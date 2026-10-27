package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/team"
)

// TeamStore реализует team.Store поверх database/sql.
type TeamStore struct{ DB *sql.DB }

var _ team.Store = TeamStore{}

// CreateTeam создаёт команду, членство owner и ревизию кеша одной транзакцией: при ошибке любой
// вставки команды без владельца не остаётся.
func (s TeamStore) CreateTeam(ctx context.Context, t team.Team) error {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	id, owner := platform.IDBytes(t.ID), platform.IDBytes(t.CreatedBy)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO teams (id, name, created_by, created_at) VALUES (?, ?, ?, ?)`,
		id, t.Name, owner, t.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("insert team: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO team_members (team_id, user_id, role, created_at) VALUES (?, ?, 'owner', ?)`,
		id, owner, t.CreatedAt.UTC()); err != nil {
		return fmt.Errorf("insert owner membership: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO team_task_cache_versions (team_id, revision) VALUES (?, 0)`, id); err != nil {
		return fmt.Errorf("insert cache revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s TeamStore) ListTeams(ctx context.Context, userID uuid.UUID) ([]team.Team, error) {
	rows, err := s.DB.QueryContext(ctx, `
		SELECT t.id, t.name, t.created_by, t.created_at, m.role
		FROM team_members m JOIN teams t ON t.id = m.team_id
		WHERE m.user_id = ?
		ORDER BY t.id ASC`, platform.IDBytes(userID))
	if err != nil {
		return nil, fmt.Errorf("select teams: %w", err)
	}
	defer rows.Close()
	out := []team.Team{}
	for rows.Next() {
		var (
			t           team.Team
			id, creator []byte
			role        string
		)
		if err := rows.Scan(&id, &t.Name, &creator, &t.CreatedAt, &role); err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		if t.ID, err = scanID(id); err != nil {
			return nil, err
		}
		if t.CreatedBy, err = scanID(creator); err != nil {
			return nil, err
		}
		t.CreatedAt, t.Role = t.CreatedAt.UTC(), team.Role(role)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s TeamStore) ListMembers(ctx context.Context, teamID, userID uuid.UUID) ([]team.Member, error) {
	if _, member, err := memberRole(ctx, s.DB, teamID, userID); err != nil {
		return nil, err
	} else if !member {
		return nil, team.ErrNotFound
	}
	rows, err := s.DB.QueryContext(ctx, `
		SELECT m.user_id, u.name, m.role
		FROM team_members m JOIN users u ON u.id = m.user_id
		WHERE m.team_id = ?
		ORDER BY m.user_id ASC`, platform.IDBytes(teamID))
	if err != nil {
		return nil, fmt.Errorf("select members: %w", err)
	}
	defer rows.Close()
	out := []team.Member{}
	for rows.Next() {
		var (
			m    team.Member
			id   []byte
			role string
		)
		if err := rows.Scan(&id, &m.Name, &role); err != nil {
			return nil, fmt.Errorf("scan member: %w", err)
		}
		if m.UserID, err = scanID(id); err != nil {
			return nil, err
		}
		m.Role = team.Role(role)
		out = append(out, m)
	}
	return out, rows.Err()
}

// InMembershipTx — транзакция изменения состава: строка команды FOR UPDATE (см. протокол в locking.go).
func (s TeamStore) InMembershipTx(ctx context.Context, teamID uuid.UUID, fn func(team.MembershipTx) error) error {
	return inTeamTx(ctx, s.DB, teamID, lockExclusive, team.ErrNotFound, func(tx *sql.Tx) error {
		return fn(membershipTx{tx: tx, teamID: teamID})
	})
}

type membershipTx struct {
	tx     *sql.Tx
	teamID uuid.UUID
}

func (m membershipTx) Role(ctx context.Context, userID uuid.UUID) (team.Role, bool, error) {
	role, ok, err := memberRole(ctx, m.tx, m.teamID, userID)
	return team.Role(role), ok, err
}

func (m membershipTx) UserExists(ctx context.Context, userID uuid.UUID) (bool, error) {
	var n int
	if err := m.tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE id = ?`, platform.IDBytes(userID)).Scan(&n); err != nil {
		return false, fmt.Errorf("select user: %w", err)
	}
	return n > 0, nil
}

func (m membershipTx) AddMember(ctx context.Context, userID uuid.UUID, role team.Role, at time.Time) error {
	if _, err := m.tx.ExecContext(ctx,
		`INSERT INTO team_members (team_id, user_id, role, created_at) VALUES (?, ?, ?, ?)`,
		platform.IDBytes(m.teamID), platform.IDBytes(userID), string(role), at.UTC()); err != nil {
		return fmt.Errorf("insert member: %w", err)
	}
	return nil
}

func (m membershipTx) SetRole(ctx context.Context, userID uuid.UUID, role team.Role) error {
	if _, err := m.tx.ExecContext(ctx,
		`UPDATE team_members SET role = ? WHERE team_id = ? AND user_id = ?`,
		string(role), platform.IDBytes(m.teamID), platform.IDBytes(userID)); err != nil {
		return fmt.Errorf("update role: %w", err)
	}
	return nil
}

func (m membershipTx) AssignedTaskIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := m.tx.QueryContext(ctx,
		`SELECT id FROM tasks WHERE team_id = ? AND assignee_id = ? ORDER BY id FOR UPDATE`,
		platform.IDBytes(m.teamID), platform.IDBytes(userID))
	if err != nil {
		return nil, fmt.Errorf("select assigned tasks: %w", err)
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, fmt.Errorf("scan assigned task: %w", err)
		}
		id, err := platform.IDFromBytes(b)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (m membershipTx) ReleaseTask(ctx context.Context, taskID, historyID, actor uuid.UUID, changes json.RawMessage, at time.Time) error {
	if _, err := m.tx.ExecContext(ctx,
		`UPDATE tasks SET assignee_id = NULL, version = version + 1, updated_at = ? WHERE id = ? AND team_id = ?`,
		at.UTC(), platform.IDBytes(taskID), platform.IDBytes(m.teamID)); err != nil {
		return fmt.Errorf("release task: %w", err)
	}
	var version int64
	if err := m.tx.QueryRowContext(ctx, `SELECT version FROM tasks WHERE id = ?`, platform.IDBytes(taskID)).Scan(&version); err != nil {
		return fmt.Errorf("select released version: %w", err)
	}
	if _, err := m.tx.ExecContext(ctx, `
		INSERT INTO task_history (id, task_id, changed_by, task_version, changes, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		platform.IDBytes(historyID), platform.IDBytes(taskID), platform.IDBytes(actor), version, string(changes), at.UTC()); err != nil {
		return fmt.Errorf("insert history: %w", err)
	}
	return nil
}

func (m membershipTx) RemoveMember(ctx context.Context, userID uuid.UUID) error {
	if _, err := m.tx.ExecContext(ctx, `DELETE FROM team_members WHERE team_id = ? AND user_id = ?`,
		platform.IDBytes(m.teamID), platform.IDBytes(userID)); err != nil {
		return fmt.Errorf("delete member: %w", err)
	}
	return nil
}

func (m membershipTx) BumpListRevision(ctx context.Context) error {
	return bumpListRevision(ctx, m.tx, m.teamID)
}
