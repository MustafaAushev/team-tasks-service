//go:build integration

package integration

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
	mysqlstore "github.com/secunda/team-tasks-service/internal/storage/mysql"
	"github.com/secunda/team-tasks-service/internal/task"
)

// attemptStore считает попытки бизнес-транзакции и позволяет подменить операции внутри неё.
type attemptStore struct {
	task.Store
	attempts *atomic.Int32
	wrap     func(attempt int32, tx task.Tx) task.Tx
}

func (s attemptStore) InTeamTx(ctx context.Context, teamID uuid.UUID, fn func(task.Tx) error) error {
	return s.Store.InTeamTx(ctx, teamID, func(tx task.Tx) error {
		n := s.attempts.Add(1)
		if s.wrap != nil {
			tx = s.wrap(n, tx)
		}
		return fn(tx)
	})
}

// deadlockOnce: в первой попытке последний шаг записи выполняется, но транзакция получает deadlock.
type deadlockOnce struct{ task.Tx }

func (x deadlockOnce) BumpListRevision(ctx context.Context) error {
	if err := x.Tx.BumpListRevision(ctx); err != nil {
		return err
	}
	return &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock (test)"}
}

// Транзакция, откатившаяся из-за deadlock, повторяется целиком с исходной клиентской version: итог —
// одно изменение, одна запись истории и одна новая ревизия кеша.
func TestTransactionRetriedAfterDeadlock(t *testing.T) {
	e := newEnv(t)
	owner := e.user("Owner")
	tm := e.teamWith(owner, nil)
	tk := e.createTask(owner, tm, "T", nil)
	rev := e.revision(tm)

	var attempts atomic.Int32
	svc := e.taskService(attemptStore{Store: mysqlstore.TaskStore{DB: e.db}, attempts: &attempts,
		wrap: func(n int32, tx task.Tx) task.Tx {
			if n == 1 {
				return deadlockOnce{tx}
			}
			return tx
		}})
	got, err := svc.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: 1, Title: task.Set("После повтора")})
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 2 || got.Version != 2 || e.version(tk.ID) != 2 || e.historyCount(tk.ID) != 2 || e.revision(tm) != rev+1 {
		t.Fatalf("попыток %d, версия %d, история %d, ревизия %d→%d", attempts.Load(), e.version(tk.ID), e.historyCount(tk.ID), rev, e.revision(tm))
	}
}

// Конфликт версии — бизнес-результат: сервер не повторяет транзакцию сам.
func TestVersionConflictIsNotRetried(t *testing.T) {
	e := newEnv(t)
	owner := e.user("Owner")
	tm := e.teamWith(owner, nil)
	tk := e.createTask(owner, tm, "T", nil)
	var attempts atomic.Int32
	svc := e.taskService(attemptStore{Store: mysqlstore.TaskStore{DB: e.db}, attempts: &attempts})
	if _, err := svc.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: 5, Title: task.Set("x")}); !errors.As(err, new(*task.VersionConflictError)) {
		t.Fatalf("ожидался конфликт: %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("конфликт повторён: %d попыток", attempts.Load())
	}
}

// Блокировка команды держится дольше всех попыток: после трёх тайм-аутов ожидания — отказ зависимости
// (503 на HTTP-уровне), без частичных изменений и без ложного конфликта версии.
func TestLockTimeoutRetriesExhausted(t *testing.T) {
	e := newEnv(t)
	owner := e.user("Owner")
	tm := e.teamWith(owner, nil)
	tk := e.createTask(owner, tm, "T", nil)

	cfg := e.cfg
	cfg.MySQLLockWait = time.Second // минимальный innodb_lock_wait_timeout
	db, err := mysqlstore.Open(e.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	holder, err := e.db.BeginTx(e.ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback() }()
	var one int
	if err := holder.QueryRow(`SELECT 1 FROM teams WHERE id = ? FOR UPDATE`, platform.IDBytes(tm)).Scan(&one); err != nil {
		t.Fatal(err)
	}

	svc := e.taskService(mysqlstore.TaskStore{DB: db})
	start := time.Now()
	_, err = svc.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: 1, Title: task.Set("x")})
	elapsed := time.Since(start)
	if err == nil || !mysqlstore.IsUnavailable(err) || errors.As(err, new(*task.VersionConflictError)) {
		t.Fatalf("ожидался отказ зависимости: %v", err)
	}
	if elapsed < 3*time.Second || elapsed > 6*time.Second {
		t.Fatalf("ожидалось три попытки по ~1 с, прошло %v", elapsed)
	}
	_ = holder.Rollback()
	if e.version(tk.ID) != 1 || e.historyCount(tk.ID) != 1 {
		t.Fatal("частичные изменения после отказа")
	}
}

// cancelBeforeHistory отменяет запрос посреди транзакции: задача уже обновлена, история — нет.
type cancelBeforeHistory struct {
	task.Tx
	cancel context.CancelFunc
}

func (x cancelBeforeHistory) InsertHistory(ctx context.Context, h task.HistoryEntry) error {
	x.cancel()
	return x.Tx.InsertHistory(ctx, h)
}

// Отмена клиентом посреди транзакции откатывает всё и не повторяется.
func TestCancellationLeavesNoPartialData(t *testing.T) {
	e := newEnv(t)
	owner := e.user("Owner")
	tm := e.teamWith(owner, nil)
	tk := e.createTask(owner, tm, "T", nil)
	rev := e.revision(tm)

	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	var attempts atomic.Int32
	svc := e.taskService(attemptStore{Store: mysqlstore.TaskStore{DB: e.db}, attempts: &attempts,
		wrap: func(_ int32, tx task.Tx) task.Tx { return cancelBeforeHistory{Tx: tx, cancel: cancel} }})
	_, err := svc.Update(ctx, owner, tk.ID, task.UpdateInput{Version: 1, Title: task.Set("Не сохранится")})
	if err == nil || !mysqlstore.IsUnavailable(err) {
		t.Fatalf("ожидалась ошибка отмены: %v", err)
	}
	if attempts.Load() != 1 || e.version(tk.ID) != 1 || e.historyCount(tk.ID) != 1 || e.revision(tm) != rev {
		t.Fatalf("после отмены: попыток %d, версия %d, история %d", attempts.Load(), e.version(tk.ID), e.historyCount(tk.ID))
	}
	if e.count(`SELECT COUNT(*) FROM tasks WHERE id = ? AND title = 'T'`, platform.IDBytes(tk.ID)) != 1 {
		t.Fatal("заголовок изменён")
	}
}
