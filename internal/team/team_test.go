package team

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
)

// Матрица полномочий команды: каждая роль против каждого действия управления составом и задачами.
func TestTeamRolePermissions(t *testing.T) {
	type perms struct{ invite, changeRoles, removeMembers, manageAllTasks, viewStats bool }
	want := map[Role]perms{
		RoleOwner:  {invite: true, changeRoles: true, removeMembers: true, manageAllTasks: true, viewStats: true},
		RoleAdmin:  {invite: true, changeRoles: false, removeMembers: true, manageAllTasks: true, viewStats: true},
		RoleMember: {},
		Role(""):   {},
		Role("x"):  {},
	}
	for role, w := range want {
		got := perms{CanInvite(role), CanChangeRoles(role), CanRemoveMembers(role), CanManageAllTasks(role), CanViewStats(role)}
		if got != w {
			t.Errorf("%q: получено %+v, ожидалось %+v", role, got, w)
		}
	}
	for role, ok := range map[Role]bool{RoleAdmin: true, RoleMember: true, RoleOwner: false, "boss": false} {
		if IsAssignableRole(role) != ok {
			t.Errorf("IsAssignableRole(%q) != %v", role, ok)
		}
	}
}

func TestParseRole(t *testing.T) {
	for _, s := range []string{"owner", "admin", "member"} {
		if r, ok := ParseRole(s); !ok || string(r) != s {
			t.Errorf("ParseRole(%q) = %q, %v", s, r, ok)
		}
	}
	for _, s := range []string{"", "Owner", "root"} {
		if _, ok := ParseRole(s); ok {
			t.Errorf("ParseRole(%q) должен отклоняться", s)
		}
	}
}

// panicStore: проверки, которые должны завершиться до обращения к хранилищу.
type panicStore struct{ Store }

func TestServiceValidatesBeforeStore(t *testing.T) {
	clock := &platform.FixedClock{T: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s := NewService(panicStore{}, platform.UUIDv7Generator{}, clock)
	ctx, actor := context.Background(), uuid.Must(uuid.NewV7())

	for name, in := range map[string]string{"пусто": "", "пробелы": "   ", "121 символ": strings.Repeat("я", 121)} {
		t.Run(name, func(t *testing.T) {
			var ve *platform.ValidationError
			if _, err := s.Create(ctx, actor, in); !errors.As(err, &ve) || ve.Fields["name"] == "" {
				t.Fatalf("ожидалась ошибка поля name: %v", err)
			}
		})
	}
	for _, role := range []Role{RoleOwner, "boss"} {
		var ve *platform.ValidationError
		if _, err := s.ChangeRole(ctx, actor, actor, actor, role); !errors.As(err, &ve) || ve.Fields["role"] == "" {
			t.Fatalf("роль %q: ожидалась ошибка поля role: %v", role, err)
		}
	}
}

type failingIDs struct{}

func (failingIDs) NewID() (uuid.UUID, error) { return uuid.Nil, errors.New("entropy exhausted") }

func TestCreateIDGenerationFailureWritesNothing(t *testing.T) {
	s := NewService(panicStore{}, failingIDs{}, platform.SystemClock{})
	if _, err := s.Create(context.Background(), uuid.Must(uuid.NewV7()), "Team"); err == nil {
		t.Fatal("ожидалась ошибка генерации ID")
	}
}
