//go:build integration

package integration

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/config"
	"github.com/secunda/team-tasks-service/internal/platform"
	mysqlstore "github.com/secunda/team-tasks-service/internal/storage/mysql"
	"github.com/secunda/team-tasks-service/internal/task"
	"github.com/secunda/team-tasks-service/internal/team"
	"github.com/secunda/team-tasks-service/internal/testutil"
)

// env — настоящие сервисы команд и задач на изолированной базе MySQL.
type env struct {
	t     *testing.T
	ctx   context.Context
	db    *sql.DB
	cfg   config.Config
	ids   *testutil.QueueIDs
	clock *platform.FixedClock
	teams *team.Service
	tasks *task.Service
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db, cfg := testutil.MySQL(t)
	e := &env{t: t, ctx: context.Background(), db: db, cfg: cfg, ids: &testutil.QueueIDs{},
		clock: &platform.FixedClock{T: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)}}
	e.teams = team.NewService(mysqlstore.TeamStore{DB: db}, e.ids, e.clock)
	e.tasks = e.taskService(mysqlstore.TaskStore{DB: db})
	return e
}

// taskService — сервис задач поверх произвольного хранилища (например, с точками синхронизации).
func (e *env) taskService(store task.Store) *task.Service {
	return task.NewService(store, e.ids, e.clock, e.cfg.CursorSecret)
}

// user вставляет пользователя напрямую: регистрация проверяется отдельными тестами.
func (e *env) user(name string) uuid.UUID {
	e.t.Helper()
	id := mustID(e.t)
	mustExecDB(e.t, e.db, `INSERT INTO users (id, email, password_hash, name, created_at) VALUES (?, ?, 'x', ?, NOW(6))`,
		platform.IDBytes(id), id.String()+"@example.com", name)
	return id
}

// teamWith создаёт команду владельца owner и добавляет участников с заданными ролями.
func (e *env) teamWith(owner uuid.UUID, members map[uuid.UUID]team.Role) uuid.UUID {
	e.t.Helper()
	tm, err := e.teams.Create(e.ctx, owner, "Team")
	if err != nil {
		e.t.Fatal(err)
	}
	for id, role := range members {
		if _, err := e.teams.Invite(e.ctx, tm.ID, owner, id); err != nil {
			e.t.Fatal(err)
		}
		if role != team.RoleMember {
			if _, err := e.teams.ChangeRole(e.ctx, tm.ID, owner, id, role); err != nil {
				e.t.Fatal(err)
			}
		}
	}
	return tm.ID
}

func (e *env) createTask(actor, teamID uuid.UUID, title string, assignee *uuid.UUID) task.Task {
	e.t.Helper()
	tk, err := e.tasks.Create(e.ctx, actor, task.CreateInput{TeamID: teamID, Title: title, AssigneeID: assignee})
	if err != nil {
		e.t.Fatal(err)
	}
	return tk
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.db.QueryRow(q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (e *env) historyCount(taskID uuid.UUID) int {
	return e.count(`SELECT COUNT(*) FROM task_history WHERE task_id = ?`, platform.IDBytes(taskID))
}

func (e *env) revision(teamID uuid.UUID) int {
	return e.count(`SELECT revision FROM team_task_cache_versions WHERE team_id = ?`, platform.IDBytes(teamID))
}

func (e *env) version(taskID uuid.UUID) int {
	return e.count(`SELECT version FROM tasks WHERE id = ?`, platform.IDBytes(taskID))
}

func mustExecDB(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// hookStore оборачивает настоящее хранилище задач и вызывает beforeUpdate внутри бизнес-транзакции
// непосредственно перед условным UPDATE — после того как задача прочитана и права проверены.
type hookStore struct {
	task.Store
	beforeUpdate func()
}

func (s hookStore) InTeamTx(ctx context.Context, teamID uuid.UUID, fn func(task.Tx) error) error {
	return s.Store.InTeamTx(ctx, teamID, func(tx task.Tx) error {
		return fn(hookTx{Tx: tx, before: s.beforeUpdate})
	})
}

type hookTx struct {
	task.Tx
	before func()
}

func (x hookTx) UpdateIfVersion(ctx context.Context, t task.Task, expected int64) (bool, error) {
	if x.before != nil {
		x.before()
	}
	return x.Tx.UpdateIfVersion(ctx, t, expected)
}
