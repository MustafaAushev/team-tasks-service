//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/history"
	"github.com/secunda/team-tasks-service/internal/platform"
	mysqlstore "github.com/secunda/team-tasks-service/internal/storage/mysql"
	"github.com/secunda/team-tasks-service/internal/task"
	"github.com/secunda/team-tasks-service/internal/team"
	"github.com/secunda/team-tasks-service/internal/testutil"
)

func (e *env) isMember(teamID, userID uuid.UUID) bool {
	return e.count(`SELECT COUNT(*) FROM team_members WHERE team_id = ? AND user_id = ?`,
		platform.IDBytes(teamID), platform.IDBytes(userID)) == 1
}

func (e *env) assignedTo(teamID, userID uuid.UUID) int {
	return e.count(`SELECT COUNT(*) FROM tasks WHERE team_id = ? AND assignee_id = ?`,
		platform.IDBytes(teamID), platform.IDBytes(userID))
}

func (e *env) totalHistory() int { return e.count(`SELECT COUNT(*) FROM task_history`) }

// Матрица отправителя и цели: права проверяются до сведений о цели и её назначениях, owner защищён,
// самостоятельного выхода нет, повтор для уже исключённого — успех без изменений.
func TestRemoveMemberMatrix(t *testing.T) {
	e := newEnv(t)
	owner, admin, admin2 := e.user("Owner"), e.user("Admin"), e.user("Admin2")
	m1, m2, assigned, outsider, stranger := e.user("M1"), e.user("M2"), e.user("Busy"), e.user("Out"), e.user("Never")
	tm := e.teamWith(owner, map[uuid.UUID]team.Role{admin: team.RoleAdmin, admin2: team.RoleAdmin,
		m1: team.RoleMember, m2: team.RoleMember, assigned: team.RoleMember})
	e.createTask(owner, tm, "Назначенная", &assigned)
	ownerTask := e.createTask(owner, tm, "Задача владельца", &owner)

	for name, c := range map[string]struct {
		actor, target uuid.UUID
		want          error
	}{
		"неучастник": {outsider, m1, team.ErrNotFound},
		"member":     {m2, m1, team.ErrForbidden},
		"member о назначенном участнике": {m2, assigned, team.ErrForbidden}, // не 409: число назначений не раскрывается
		"member исключает себя":          {m2, m2, team.ErrForbidden},
		"admin исключает owner":          {admin, owner, team.ErrOwnerCannotBeRemoved},
		"owner исключает себя":           {owner, owner, team.ErrOwnerCannotBeRemoved},
		"admin исключает себя":           {admin, admin, team.ErrSelfRemoval},
	} {
		if err := e.teams.Remove(e.ctx, tm, c.actor, c.target, true); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, ожидалось %v", name, err, c.want)
		}
	}
	if err := e.teams.Remove(e.ctx, mustID(t), owner, m1, true); !errors.Is(err, team.ErrNotFound) {
		t.Errorf("несуществующая команда: %v", err)
	}
	if !e.isMember(tm, owner) || e.assignedTo(tm, owner) != 1 || e.version(ownerTask.ID) != 1 {
		t.Fatal("отказ изменил владельца или его назначения")
	}

	// admin исключает admin без назначений: подтверждение не требуется.
	if err := e.teams.Remove(e.ctx, tm, admin, admin2, false); err != nil || e.isMember(tm, admin2) {
		t.Fatalf("admin исключает admin: %v", err)
	}
	// Повтор успешного исключения — успех без новых записей; права отправителя проверяются и при повторе.
	if err := e.teams.Remove(e.ctx, tm, owner, m1, false); err != nil {
		t.Fatal(err)
	}
	historyCount, rev := e.totalHistory(), e.revision(tm)
	if err := e.teams.Remove(e.ctx, tm, owner, m1, false); err != nil {
		t.Fatalf("повтор: %v", err)
	}
	if err := e.teams.Remove(e.ctx, tm, owner, stranger, true); err != nil {
		t.Fatalf("никогда не состоявший: %v", err)
	}
	if e.totalHistory() != historyCount || e.revision(tm) != rev {
		t.Fatal("повтор создал изменения")
	}
	if err := e.teams.Remove(e.ctx, tm, m2, m1, false); !errors.Is(err, team.ErrForbidden) {
		t.Fatalf("повтор от member: %v", err)
	}
	if err := e.teams.Remove(e.ctx, tm, m1, m2, false); !errors.Is(err, team.ErrNotFound) {
		t.Fatalf("исключённый больше не может исключать: %v", err)
	}
}

// Двухшаговое исключение: предупреждение без изменений, затем подтверждение снимает все назначения
// на момент второго запроса — с version, историей от имени исключающего и сбросом кеша.
func TestRemoveMemberConfirmation(t *testing.T) {
	e := newEnv(t)
	owner, admin, target, other, idle := e.user("Owner"), e.user("Admin"), e.user("Target"), e.user("Other"), e.user("Idle")
	tm := e.teamWith(owner, map[uuid.UUID]team.Role{admin: team.RoleAdmin, target: team.RoleMember, other: team.RoleMember,
		idle: team.RoleMember})
	second := e.teamWith(owner, map[uuid.UUID]team.Role{target: team.RoleMember})

	todo := e.createTask(owner, tm, "todo", &target)
	progress := e.taskAt(e.clock.Now(), owner, tm, &target).status(e.clock.Now(), "in_progress")
	closed := e.taskAt(e.clock.Now(), owner, tm, &target).status(e.clock.Now(), "done")
	authored := e.createTask(target, tm, "Автор — исключаемый", &other)
	elsewhere := e.createTask(owner, second, "Другая команда", &target)

	versions := map[uuid.UUID]int{todo.ID: e.version(todo.ID), progress.t.ID: e.version(progress.t.ID), closed.t.ID: e.version(closed.t.ID)}
	historyCount, rev := e.totalHistory(), e.revision(tm)

	// Без подтверждения: 409 с числом, ничего не меняется.
	var at *team.AssignedTasksError
	if err := e.teams.Remove(e.ctx, tm, admin, target, false); !errors.As(err, &at) || at.Count != 3 {
		t.Fatalf("предупреждение: %v", err)
	}
	if !e.isMember(tm, target) || e.assignedTo(tm, target) != 3 || e.totalHistory() != historyCount || e.revision(tm) != rev {
		t.Fatal("предупреждение изменило данные")
	}

	// Между предупреждением и подтверждением появилось ещё одно назначение — подтверждение снимает и его.
	late := e.createTask(owner, tm, "После предупреждения", &target)
	versions[late.ID] = e.version(late.ID)
	historyCount, rev = e.totalHistory(), e.revision(tm)
	if err := e.teams.Remove(e.ctx, tm, admin, target, true); err != nil {
		t.Fatal(err)
	}
	if e.isMember(tm, target) || e.assignedTo(tm, target) != 0 {
		t.Fatal("участник или назначения остались")
	}
	if e.totalHistory() != historyCount+4 || e.revision(tm) != rev+1 {
		t.Fatalf("история %d → %d, ревизия %d → %d", historyCount, e.totalHistory(), rev, e.revision(tm))
	}
	for id, v := range versions {
		if e.version(id) != v+1 {
			t.Errorf("версия %s: %d, ожидалась %d", id, e.version(id), v+1)
		}
		var changedBy []byte
		var changes string
		if err := e.db.QueryRow(`SELECT changed_by, changes FROM task_history WHERE task_id = ? AND task_version = ?`,
			platform.IDBytes(id), v+1).Scan(&changedBy, &changes); err != nil {
			t.Fatal(err)
		}
		var got map[string]history.Change
		if err := json.Unmarshal([]byte(changes), &got); err != nil {
			t.Fatal(err)
		}
		if by, _ := platform.IDFromBytes(changedBy); by != admin || len(got) != 1 ||
			got["assignee_id"].Old != platform.IDString(target) || got["assignee_id"].New != nil {
			t.Errorf("журнал %s: %s от %v", id, changes, changedBy)
		}
	}
	// Закрытая задача осталась закрытой с прежней датой; автор задачи и другая команда не затронуты.
	if e.count(`SELECT COUNT(*) FROM tasks WHERE id = ? AND status = 'done' AND closed_at IS NOT NULL`, platform.IDBytes(closed.t.ID)) != 1 {
		t.Error("закрытая задача изменила статус")
	}
	if e.count(`SELECT COUNT(*) FROM tasks WHERE id = ? AND created_by = ? AND assignee_id = ?`,
		platform.IDBytes(authored.ID), platform.IDBytes(target), platform.IDBytes(other)) != 1 {
		t.Error("авторство или чужое назначение изменены")
	}
	if e.assignedTo(second, target) != 1 || !e.isMember(second, target) || e.version(elsewhere.ID) != 1 {
		t.Error("затронута другая команда")
	}
	if e.count(`SELECT COUNT(*) FROM users WHERE id = ?`, platform.IDBytes(target)) != 1 {
		t.Error("учётная запись удалена")
	}

	// Без назначений подтверждение не требуется.
	if err := e.teams.Remove(e.ctx, tm, owner, idle, false); err != nil || e.isMember(tm, idle) {
		t.Fatalf("исключение без назначений: %v", err)
	}
	// Owner читал задачу до исключения исполнителя: PUT со старой версией — конфликт, новое чтение нужно.
	if _, err := e.tasks.Update(e.ctx, owner, todo.ID, task.UpdateInput{Version: int64(versions[todo.ID]), Title: task.Set("x")}); !errors.As(err, new(*task.VersionConflictError)) {
		t.Fatalf("PUT со старой версией: %v", err)
	}
}

// Ошибка записи истории одной из задач откатывает снятие всех назначений и удаление членства.
func TestRemoveMemberRollsBackOnHistoryFailure(t *testing.T) {
	e := newEnv(t)
	owner, target := e.user("Owner"), e.user("Target")
	tm := e.teamWith(owner, map[uuid.UUID]team.Role{target: team.RoleMember})
	a := e.createTask(owner, tm, "A", &target)
	b := e.createTask(owner, tm, "B", &target)
	historyCount, rev := e.totalHistory(), e.revision(tm)

	restore := testutil.FailInserts(t, e.cfg, "task_history")
	err := e.teams.Remove(e.ctx, tm, owner, target, true)
	restore()
	if err == nil {
		t.Fatal("ожидалась ошибка истории")
	}
	if !e.isMember(tm, target) || e.assignedTo(tm, target) != 2 || e.version(a.ID) != 1 || e.version(b.ID) != 1 ||
		e.totalHistory() != historyCount || e.revision(tm) != rev {
		t.Fatal("частичное исключение не откатилось")
	}
}

// holdMembership выполняет hold внутри транзакции изменения состава сразу после блокировки команды.
type holdMembership struct {
	team.Store
	hold func()
}

func (s holdMembership) InMembershipTx(ctx context.Context, teamID uuid.UUID, fn func(team.MembershipTx) error) error {
	return s.Store.InMembershipTx(ctx, teamID, func(tx team.MembershipTx) error {
		s.hold()
		return fn(tx)
	})
}

// Гонка исключения с назначением: назначение либо завершается до исключения и снимается им, либо
// ждёт исключения и отклоняется. Недопустимого исполнителя не остаётся.
func TestMemberRemovalRacesWithAssignment(t *testing.T) {
	t.Run("исключение первым", func(t *testing.T) {
		e := newEnv(t)
		owner, target := e.user("Owner"), e.user("Target")
		tm := e.teamWith(owner, map[uuid.UUID]team.Role{target: team.RoleMember})
		existing := e.createTask(owner, tm, "Переназначаемая", nil)

		locked, release := make(chan struct{}), make(chan struct{})
		teams := team.NewService(holdMembership{Store: mysqlstore.TeamStore{DB: e.db}, hold: func() {
			close(locked)
			<-release
		}}, e.ids, e.clock)
		removed := make(chan error, 1)
		go func() { removed <- teams.Remove(e.ctx, tm, owner, target, true) }()
		<-locked

		created, reassigned := make(chan error, 1), make(chan error, 1)
		go func() {
			_, err := e.tasks.Create(e.ctx, owner, task.CreateInput{TeamID: tm, Title: "Новая", AssigneeID: &target})
			created <- err
		}()
		go func() {
			_, err := e.tasks.Update(e.ctx, owner, existing.ID, task.UpdateInput{Version: 1, AssigneeID: task.Set(target)})
			reassigned <- err
		}()
		testutil.WaitForLockWaits(t, e.cfg, 2) // обе записи ждут блокировку команды
		close(release)

		if err := <-removed; err != nil {
			t.Fatal(err)
		}
		for name, ch := range map[string]chan error{"создание": created, "переназначение": reassigned} {
			if err := <-ch; !errors.Is(err, task.ErrAssigneeNotInTeam) {
				t.Errorf("%s после исключения: %v", name, err)
			}
		}
		if e.assignedTo(tm, target) != 0 || e.version(existing.ID) != 1 {
			t.Fatal("осталось недопустимое назначение")
		}
	})

	t.Run("назначение первым", func(t *testing.T) {
		e := newEnv(t)
		owner, target := e.user("Owner"), e.user("Target")
		tm := e.teamWith(owner, map[uuid.UUID]team.Role{target: team.RoleMember})
		existing := e.createTask(owner, tm, "Переназначаемая", nil)

		locked, release := make(chan struct{}), make(chan struct{})
		svc := e.taskService(hookStore{Store: mysqlstore.TaskStore{DB: e.db}, beforeUpdate: func() {
			close(locked)
			<-release
		}})
		reassigned := make(chan error, 1)
		go func() {
			_, err := svc.Update(e.ctx, owner, existing.ID, task.UpdateInput{Version: 1, AssigneeID: task.Set(target)})
			reassigned <- err
		}()
		<-locked // запись держит блокировку команды FOR SHARE

		removed := make(chan error, 1)
		go func() { removed <- e.teams.Remove(e.ctx, tm, owner, target, true) }()
		testutil.WaitForLockWaits(t, e.cfg, 1)
		close(release)

		if err := <-reassigned; err != nil {
			t.Fatal(err)
		}
		if err := <-removed; err != nil {
			t.Fatal(err)
		}
		// Назначение успело до исключения и было снято им: версия 2 (назначение) → 3 (снятие).
		if e.assignedTo(tm, target) != 0 || e.version(existing.ID) != 3 || e.isMember(tm, target) {
			t.Fatalf("назначение не снято: версия %d", e.version(existing.ID))
		}
	})
}
