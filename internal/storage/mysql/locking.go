package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
)

// Протокол блокировок команды.
//
// Строка teams — ворота изменения состава. Каждая изменяющая бизнес-транзакция выполняется в
// READ COMMITTED и ПЕРВЫМ шагом блокирует строку своей команды, удерживая её до конца:
//
//   - запись задач и комментариев — FOR SHARE (lockShared): записи разных задач идут параллельно,
//     а после получения блокировки членство и роли читаются в актуальном состоянии;
//   - приглашение, смена роли, исключение — FOR UPDATE (lockExclusive): ждёт уже идущие записи
//     задач команды и не пускает новые, пока состав меняется.
//
// Дальнейший порядок: задача (условный UPDATE или блокировки задач по id ASC), история, ревизия
// кеша списков. Эксклюзивная блокировка teams после общей в одной транзакции не берётся — это
// исключает взаимоблокировку повышения блокировки.
type lockMode int

const (
	lockShared lockMode = iota
	lockExclusive
)

// maxTxRetries — сколько раз повторяется транзакция, откатившаяся из-за взаимоблокировки или
// тайм-аута ожидания блокировки (всего до трёх попыток).
const maxTxRetries = 2

// inTeamTx открывает транзакцию по протоколу выше. Если строки команды нет — notFound.
// Ошибка fn или блокировки откатывает транзакцию; иначе выполняется commit. Технически откатившаяся
// транзакция (deadlock, lock wait timeout) повторяется целиком до maxTxRetries раз с коротким jitter,
// пока позволяет бюджет запроса: fn выполняется заново с теми же входными данными. Бизнес-ошибки
// (конфликт версии, права) и ошибки commit не повторяются.
func inTeamTx(ctx context.Context, db *sql.DB, teamID uuid.UUID, mode lockMode, notFound error, fn func(*sql.Tx) error) error {
	for attempt := 0; ; attempt++ {
		err := teamTxOnce(ctx, db, teamID, mode, notFound, fn)
		if err == nil || attempt == maxTxRetries || !retryable(err) {
			return err
		}
		pause := time.Duration(attempt+1) * time.Duration(5+rand.IntN(20)) * time.Millisecond
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < pause {
			return err
		}
		select {
		case <-time.After(pause):
		case <-ctx.Done():
			return err
		}
	}
}

func teamTxOnce(ctx context.Context, db *sql.DB, teamID uuid.UUID, mode lockMode, notFound error, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // после Commit — no-op

	q := `SELECT 1 FROM teams WHERE id = ? FOR SHARE`
	if mode == lockExclusive {
		q = `SELECT 1 FROM teams WHERE id = ? FOR UPDATE`
	}
	var one int
	switch err := tx.QueryRowContext(ctx, q, platform.IDBytes(teamID)).Scan(&one); {
	case errors.Is(err, sql.ErrNoRows):
		return notFound
	case err != nil:
		return fmt.Errorf("lock team: %w", err)
	}

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return &commitError{err: err}
	}
	return nil
}

// memberRole читает роль пользователя в команде; false — не участник.
func memberRole(ctx context.Context, q queryer, teamID, userID uuid.UUID) (string, bool, error) {
	var role string
	err := q.QueryRowContext(ctx, `SELECT role FROM team_members WHERE team_id = ? AND user_id = ?`,
		platform.IDBytes(teamID), platform.IDBytes(userID)).Scan(&role)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", false, nil
	case err != nil:
		return "", false, fmt.Errorf("select member role: %w", err)
	}
	return role, true, nil
}

// queryer — общее у *sql.DB и *sql.Tx.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// scanID читает BINARY(16) в UUID.
func scanID(raw []byte) (uuid.UUID, error) { return platform.IDFromBytes(raw) }

// nullableID читает nullable BINARY(16).
func nullableID(raw []byte) (*uuid.UUID, error) {
	if raw == nil {
		return nil, nil
	}
	id, err := platform.IDFromBytes(raw)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// nullableIDBytes — значение для nullable BINARY(16).
func nullableIDBytes(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return platform.IDBytes(*id)
}
