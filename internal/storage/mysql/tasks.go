package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/task"
	"github.com/secunda/team-tasks-service/internal/team"
)

// TaskStore реализует task.Store поверх database/sql.
type TaskStore struct{ DB *sql.DB }

var _ task.Store = TaskStore{}

const taskColumns = `t.id, t.team_id, t.title, t.description, t.status, t.created_by, t.assignee_id,
	t.version, t.created_at, t.updated_at, t.closed_at`

type rowScanner interface{ Scan(dest ...any) error }

func scanTask(r rowScanner) (task.Task, error) {
	var (
		t                        task.Task
		id, teamID, by, assignee []byte
		status                   string
		closed                   sql.NullTime
	)
	if err := r.Scan(&id, &teamID, &t.Title, &t.Description, &status, &by, &assignee,
		&t.Version, &t.CreatedAt, &t.UpdatedAt, &closed); err != nil {
		return task.Task{}, err
	}
	var err error
	if t.ID, err = scanID(id); err != nil {
		return task.Task{}, err
	}
	if t.TeamID, err = scanID(teamID); err != nil {
		return task.Task{}, err
	}
	if t.CreatedBy, err = scanID(by); err != nil {
		return task.Task{}, err
	}
	if t.AssigneeID, err = nullableID(assignee); err != nil {
		return task.Task{}, err
	}
	t.Status = task.Status(status)
	t.CreatedAt, t.UpdatedAt = t.CreatedAt.UTC(), t.UpdatedAt.UTC()
	if closed.Valid {
		c := closed.Time.UTC()
		t.ClosedAt = &c
	}
	return t, nil
}

func scanTasks(rows *sql.Rows) ([]task.Task, error) {
	defer rows.Close()
	out := []task.Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s TaskStore) TaskTeam(ctx context.Context, taskID uuid.UUID) (uuid.UUID, error) {
	var raw []byte
	err := s.DB.QueryRowContext(ctx, `SELECT team_id FROM tasks WHERE id = ?`, platform.IDBytes(taskID)).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return uuid.Nil, task.ErrNotFound
	case err != nil:
		return uuid.Nil, fmt.Errorf("select task team: %w", err)
	}
	return scanID(raw)
}

// InTeamTx — бизнес-транзакция записи задач: строка команды FOR SHARE (см. протокол в locking.go).
func (s TaskStore) InTeamTx(ctx context.Context, teamID uuid.UUID, fn func(task.Tx) error) error {
	return inTeamTx(ctx, s.DB, teamID, lockShared, task.ErrNotFound, func(tx *sql.Tx) error {
		return fn(taskTx{tx: tx, teamID: teamID})
	})
}

func (s TaskStore) VisibleTask(ctx context.Context, taskID, userID uuid.UUID) (task.Task, error) {
	t, err := scanTask(s.DB.QueryRowContext(ctx, `
		SELECT `+taskColumns+`
		FROM tasks t JOIN team_members m ON m.team_id = t.team_id AND m.user_id = ?
		WHERE t.id = ?`, platform.IDBytes(userID), platform.IDBytes(taskID)))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return task.Task{}, task.ErrNotFound
	case err != nil:
		return task.Task{}, fmt.Errorf("select task: %w", err)
	}
	return t, nil
}

// filterSQL строит условие выборки; фильтры объединяются по И.
func filterSQL(f task.Filter) (string, []any) {
	where := []string{"t.team_id = ?"}
	args := []any{platform.IDBytes(f.TeamID)}
	if f.Status != nil {
		where = append(where, "t.status = ?")
		args = append(args, string(*f.Status))
	}
	if f.AssigneeID != nil {
		where = append(where, "t.assignee_id = ?")
		args = append(args, platform.IDBytes(*f.AssigneeID))
	}
	return strings.Join(where, " AND "), args
}

// ListRevision проверяет членство и читает текущую ревизию списков команды одним запросом.
func (s TaskStore) ListRevision(ctx context.Context, teamID, userID uuid.UUID) (int64, error) {
	return listRevision(ctx, s.DB, teamID, userID)
}

// listRevision — ревизия списков команды, если userID в ней состоит; иначе task.ErrNotFound.
func listRevision(ctx context.Context, q queryer, teamID, userID uuid.UUID) (int64, error) {
	var rev int64
	err := q.QueryRowContext(ctx, `SELECT v.revision FROM team_members m
		JOIN team_task_cache_versions v ON v.team_id = m.team_id
		WHERE m.team_id = ? AND m.user_id = ?`, platform.IDBytes(teamID), platform.IDBytes(userID)).Scan(&rev)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, task.ErrNotFound
	case err != nil:
		return 0, fmt.Errorf("select list revision: %w", err)
	}
	return rev, nil
}

// listSnapshot выполняет fn в короткой read-only транзакции REPEATABLE READ: членство, ревизия
// списков, страница и total читаются из одного снимка. Поэтому страница, записанная в кеш под этой
// ревизией, не может содержать состояние новее или старее неё. Бизнес-записи остаются READ COMMITTED.
func (s TaskStore) listSnapshot(ctx context.Context, f task.Filter, userID uuid.UUID, fn func(*sql.Tx) error) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rev, err := listRevision(ctx, tx, f.TeamID, userID)
	if err != nil {
		return 0, err
	}
	if err := fn(tx); err != nil {
		return 0, err
	}
	return rev, tx.Commit()
}

func (s TaskStore) ListOffset(ctx context.Context, userID uuid.UUID, f task.Filter, limit, offset int) ([]task.Task, int, int64, error) {
	var (
		items []task.Task
		total int
	)
	rev, err := s.listSnapshot(ctx, f, userID, func(tx *sql.Tx) error {
		where, args := filterSQL(f)
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks t WHERE `+where, args...).Scan(&total); err != nil {
			return fmt.Errorf("count tasks: %w", err)
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks t WHERE `+where+
			` ORDER BY t.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
		if err != nil {
			return fmt.Errorf("select tasks: %w", err)
		}
		items, err = scanTasks(rows)
		return err
	})
	if err != nil {
		return nil, 0, 0, err
	}
	return items, total, rev, nil
}

func (s TaskStore) ListBefore(ctx context.Context, userID uuid.UUID, f task.Filter, before *uuid.UUID, limit int) ([]task.Task, int64, error) {
	var items []task.Task
	rev, err := s.listSnapshot(ctx, f, userID, func(tx *sql.Tx) error {
		where, args := filterSQL(f)
		if before != nil {
			where += " AND t.id < ?"
			args = append(args, platform.IDBytes(*before))
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks t WHERE `+where+
			` ORDER BY t.id DESC LIMIT ?`, append(args, limit)...)
		if err != nil {
			return fmt.Errorf("select tasks: %w", err)
		}
		items, err = scanTasks(rows)
		return err
	})
	if err != nil {
		return nil, 0, err
	}
	return items, rev, nil
}

func (s TaskStore) History(ctx context.Context, taskID uuid.UUID, afterVersion int64, limit int) ([]task.HistoryEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT id, task_id, changed_by, task_version, changes, created_at
		FROM task_history
		WHERE task_id = ? AND task_version > ?
		ORDER BY task_version ASC
		LIMIT ?`, platform.IDBytes(taskID), afterVersion, limit)
	if err != nil {
		return nil, fmt.Errorf("select history: %w", err)
	}
	defer rows.Close()
	out := []task.HistoryEntry{}
	for rows.Next() {
		var (
			h           task.HistoryEntry
			id, tid, by []byte
			changes     []byte
		)
		if err := rows.Scan(&id, &tid, &by, &h.TaskVersion, &changes, &h.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan history: %w", err)
		}
		if h.ID, err = scanID(id); err != nil {
			return nil, err
		}
		if h.TaskID, err = scanID(tid); err != nil {
			return nil, err
		}
		if h.ChangedBy, err = scanID(by); err != nil {
			return nil, err
		}
		h.Changes, h.CreatedAt = changes, h.CreatedAt.UTC()
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s TaskStore) Comments(ctx context.Context, taskID uuid.UUID, after *uuid.UUID, limit int) ([]task.Comment, error) {
	where, args := "task_id = ?", []any{platform.IDBytes(taskID)}
	if after != nil {
		where += " AND id > ?"
		args = append(args, platform.IDBytes(*after))
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT id, task_id, user_id, content, created_at FROM task_comments
		WHERE `+where+` ORDER BY id ASC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, fmt.Errorf("select comments: %w", err)
	}
	defer rows.Close()
	out := []task.Comment{}
	for rows.Next() {
		var (
			c            task.Comment
			id, tid, uid []byte
		)
		if err := rows.Scan(&id, &tid, &uid, &c.Content, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan comment: %w", err)
		}
		if c.ID, err = scanID(id); err != nil {
			return nil, err
		}
		if c.TaskID, err = scanID(tid); err != nil {
			return nil, err
		}
		if c.UserID, err = scanID(uid); err != nil {
			return nil, err
		}
		c.CreatedAt = c.CreatedAt.UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// taskTx — операции внутри бизнес-транзакции одной команды.
type taskTx struct {
	tx     *sql.Tx
	teamID uuid.UUID
}

func (x taskTx) MemberRole(ctx context.Context, userID uuid.UUID) (team.Role, bool, error) {
	role, ok, err := memberRole(ctx, x.tx, x.teamID, userID)
	return team.Role(role), ok, err
}

// Task — обычное чтение в READ COMMITTED, без FOR UPDATE: успех записи решает условный UPDATE.
func (x taskTx) Task(ctx context.Context, taskID uuid.UUID) (task.Task, bool, error) {
	t, err := scanTask(x.tx.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks t WHERE t.id = ? AND t.team_id = ?`,
		platform.IDBytes(taskID), platform.IDBytes(x.teamID)))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return task.Task{}, false, nil
	case err != nil:
		return task.Task{}, false, fmt.Errorf("select task: %w", err)
	}
	return t, true, nil
}

func (x taskTx) InsertTask(ctx context.Context, t task.Task) error {
	if _, err := x.tx.ExecContext(ctx, `
		INSERT INTO tasks (id, title, description, status, created_by, team_id, assignee_id, version, created_at, updated_at, closed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		platform.IDBytes(t.ID), t.Title, t.Description, string(t.Status), platform.IDBytes(t.CreatedBy),
		platform.IDBytes(x.teamID), nullableIDBytes(t.AssigneeID), t.Version, t.CreatedAt.UTC(), t.UpdatedAt.UTC(),
		nullableTime(t.ClosedAt)); err != nil {
		return fmt.Errorf("insert task: %w", err)
	}
	return nil
}

// UpdateIfVersion — атомарная проверка клиентской версии и запись. Версия в WHERE взята из запроса
// клиента: если другая транзакция успела зафиксировать изменение после нашего чтения, строка не
// совпадёт (RowsAffected = 0), и вызывающий код откатит транзакцию с 409.
func (x taskTx) UpdateIfVersion(ctx context.Context, t task.Task, expected int64) (bool, error) {
	res, err := x.tx.ExecContext(ctx, `
		UPDATE tasks
		SET title = ?, description = ?, status = ?, assignee_id = ?, closed_at = ?, updated_at = ?, version = version + 1
		WHERE id = ? AND team_id = ? AND version = ?`,
		t.Title, t.Description, string(t.Status), nullableIDBytes(t.AssigneeID), nullableTime(t.ClosedAt), t.UpdatedAt.UTC(),
		platform.IDBytes(t.ID), platform.IDBytes(x.teamID), expected)
	if err != nil {
		return false, fmt.Errorf("update task: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("update task: rows affected: %w", err)
	}
	return n == 1, nil
}

func (x taskTx) Version(ctx context.Context, taskID uuid.UUID) (int64, bool, error) {
	var v int64
	err := x.tx.QueryRowContext(ctx, `SELECT version FROM tasks WHERE id = ? AND team_id = ?`,
		platform.IDBytes(taskID), platform.IDBytes(x.teamID)).Scan(&v)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("select task version: %w", err)
	}
	return v, true, nil
}

func (x taskTx) InsertHistory(ctx context.Context, h task.HistoryEntry) error {
	if _, err := x.tx.ExecContext(ctx, `
		INSERT INTO task_history (id, task_id, changed_by, task_version, changes, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		platform.IDBytes(h.ID), platform.IDBytes(h.TaskID), platform.IDBytes(h.ChangedBy), h.TaskVersion,
		string(h.Changes), h.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("insert history: %w", err)
	}
	return nil
}

// BumpListRevision — последний шаг записи: короткая сериализация конца транзакций команды.
func (x taskTx) BumpListRevision(ctx context.Context) error {
	return bumpListRevision(ctx, x.tx, x.teamID)
}

// bumpListRevision увеличивает ревизию списков задач команды: прежние ключи кеша становятся недоступны.
func bumpListRevision(ctx context.Context, tx *sql.Tx, teamID uuid.UUID) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE team_task_cache_versions SET revision = revision + 1 WHERE team_id = ?`, platform.IDBytes(teamID)); err != nil {
		return fmt.Errorf("bump cache revision: %w", err)
	}
	return nil
}

func (x taskTx) InsertComment(ctx context.Context, c task.Comment) error {
	if _, err := x.tx.ExecContext(ctx,
		`INSERT INTO task_comments (id, task_id, user_id, content, created_at) VALUES (?, ?, ?, ?, ?)`,
		platform.IDBytes(c.ID), platform.IDBytes(c.TaskID), platform.IDBytes(c.UserID), c.Content, c.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("insert comment: %w", err)
	}
	return nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}
