package team

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
)

// fakeTx записывает шаги транзакции исключения и возвращает ошибку на шаге failOn.
type fakeTx struct {
	MembershipTx
	roles    map[uuid.UUID]Role
	assigned []uuid.UUID
	failOn   string
	steps    []string
}

var errStep = errors.New("step failed")

func (x *fakeTx) step(name string) error {
	x.steps = append(x.steps, name)
	if name == x.failOn {
		return errStep
	}
	return nil
}

func (x *fakeTx) Role(_ context.Context, id uuid.UUID) (Role, bool, error) {
	r, ok := x.roles[id]
	return r, ok, nil
}
func (x *fakeTx) AssignedTaskIDs(context.Context, uuid.UUID) ([]uuid.UUID, error) {
	return x.assigned, x.step("assigned")
}
func (x *fakeTx) ReleaseTask(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, json.RawMessage, time.Time) error {
	return x.step("release")
}
func (x *fakeTx) RemoveMember(context.Context, uuid.UUID) error { return x.step("remove") }
func (x *fakeTx) BumpListRevision(context.Context) error        { return x.step("bump") }

type fakeStore struct {
	Store
	tx *fakeTx
}

func (s fakeStore) InMembershipTx(_ context.Context, _ uuid.UUID, fn func(MembershipTx) error) error {
	return fn(s.tx)
}

// Ошибка любого шага исключения возвращается и прерывает следующие шаги: хранилище откатывает
// транзакцию, частично снятых назначений и удалённого без них членства не остаётся.
func TestRemoveStopsOnFirstError(t *testing.T) {
	owner, target := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	tasks := []uuid.UUID{uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())}
	clock := &platform.FixedClock{T: time.Unix(0, 0)}

	for name, c := range map[string]struct {
		failOn string
		ids    platform.IDGenerator
		want   []string
	}{
		"назначения":   {"assigned", platform.UUIDv7Generator{}, []string{"assigned"}},
		"снятие":       {"release", platform.UUIDv7Generator{}, []string{"assigned", "release"}},
		"членство":     {"remove", platform.UUIDv7Generator{}, []string{"assigned", "release", "release", "remove"}},
		"ревизия кеша": {"bump", platform.UUIDv7Generator{}, []string{"assigned", "release", "release", "remove", "bump"}},
		"ID истории":   {"", failingIDs{}, []string{"assigned"}},
	} {
		t.Run(name, func(t *testing.T) {
			tx := &fakeTx{roles: map[uuid.UUID]Role{owner: RoleOwner, target: RoleMember}, assigned: tasks, failOn: c.failOn}
			err := NewService(fakeStore{tx: tx}, c.ids, clock).Remove(context.Background(), uuid.Nil, owner, target, true)
			if err == nil {
				t.Fatal("ожидалась ошибка")
			}
			if !slices.Equal(tx.steps, c.want) {
				t.Fatalf("шаги %v, ожидались %v", tx.steps, c.want)
			}
		})
	}

	// Без назначений ревизия кеша не меняется: списки задач не изменились.
	tx := &fakeTx{roles: map[uuid.UUID]Role{owner: RoleOwner, target: RoleMember}}
	if err := NewService(fakeStore{tx: tx}, platform.UUIDv7Generator{}, clock).Remove(context.Background(), uuid.Nil, owner, target, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(tx.steps, []string{"assigned", "remove"}) {
		t.Fatalf("шаги без назначений: %v", tx.steps)
	}
	if (&AssignedTasksError{Count: 3}).Error() == "" {
		t.Error("пустой текст ошибки")
	}
}
