package testutil

import (
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/config"
	"github.com/secunda/team-tasks-service/internal/platform"
)

func admin(tb testing.TB) *sql.DB {
	tb.Helper()
	db, err := sql.Open("mysql", mustEnv(tb, envAdminDSN))
	if err != nil {
		tb.Fatalf("open admin mysql: %v", err)
	}
	tb.Cleanup(func() { _ = db.Close() })
	return db
}

// FailInserts создаёт в тестовой базе триггер, который отклоняет каждую вставку в table настоящей
// ошибкой MySQL. Так проверяются откаты без точек отказа в коде приложения. Возвращает функцию,
// снимающую триггер (также снимается вместе с базой после теста).
func FailInserts(tb testing.TB, cfg config.Config, table string) (restore func()) {
	tb.Helper()
	db := admin(tb)
	name := cfg.MySQLDatabase + ".fail_" + table
	stmt := "CREATE TRIGGER " + name + " BEFORE INSERT ON " + cfg.MySQLDatabase + "." + table +
		" FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'injected failure'"
	if _, err := db.Exec(stmt); err != nil {
		tb.Fatalf("create trigger: %v", err)
	}
	return func() {
		if _, err := db.Exec("DROP TRIGGER IF EXISTS " + name); err != nil {
			tb.Fatalf("drop trigger: %v", err)
		}
	}
}

// WaitForLockWaits ждёт, пока в тестовой базе n транзакций окажутся в ожидании блокировки
// (performance_schema.data_lock_waits; одна ожидающая транзакция может ждать нескольких держателей,
// поэтому считаются различные ожидающие). Это наблюдаемое состояние MySQL, а не пауза наугад: тест
// знает, что конкурент действительно заблокирован, прежде чем отпустить держателя блокировки.
func WaitForLockWaits(tb testing.TB, cfg config.Config, n int) {
	tb.Helper()
	db := admin(tb)
	deadline := time.Now().Add(10 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		var got int
		err := db.QueryRow(`
			SELECT COUNT(DISTINCT w.REQUESTING_ENGINE_TRANSACTION_ID)
			FROM performance_schema.data_lock_waits w
			JOIN performance_schema.data_locks l ON l.ENGINE_LOCK_ID = w.REQUESTING_ENGINE_LOCK_ID
			WHERE l.OBJECT_SCHEMA = ?`, cfg.MySQLDatabase).Scan(&got)
		if err != nil {
			tb.Fatalf("ожидание блокировок (нужно %d): %v", n, err)
		}
		if got >= n {
			return
		}
		if time.Now().After(deadline) {
			tb.Fatalf("за 10 с в ожидании блокировки оказалось %d транзакций из %d; активные транзакции: %s",
				got, n, describeTrx(db))
		}
		<-tick.C
	}
}

// SameMillisV7 возвращает n разных UUIDv7 с одной временной частью ms, в случайном порядке
// относительно сортировки. Нужны для проверки, что порядок страниц задаёт полный UUID.
func SameMillisV7(tb testing.TB, ms int64, n int) []uuid.UUID {
	tb.Helper()
	out := make([]uuid.UUID, 0, n)
	seen := map[uuid.UUID]bool{}
	for len(out) < n {
		id := uuid.Must(uuid.NewRandom())
		for i := 0; i < 6; i++ {
			id[i] = byte(ms >> (40 - 8*i))
		}
		id[6] = 0x70 | id[6]&0x0f // версия 7
		id[8] = 0x80 | id[8]&0x3f // вариант RFC
		if _, err := platform.ParseID(id.String()); err != nil {
			tb.Fatalf("сгенерирован некорректный UUIDv7: %v", err)
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// QueueIDs — генератор ID, выдающий заранее заданные значения, затем обычные UUIDv7.
type QueueIDs struct {
	mu  sync.Mutex
	ids []uuid.UUID
}

func (q *QueueIDs) Push(ids ...uuid.UUID) {
	q.mu.Lock()
	q.ids = append(q.ids, ids...)
	q.mu.Unlock()
}

func (q *QueueIDs) NewID() (uuid.UUID, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.ids) > 0 {
		id := q.ids[0]
		q.ids = q.ids[1:]
		return id, nil
	}
	return platform.UUIDv7Generator{}.NewID()
}

// describeTrx — диагностика для сообщения об ошибке ожидания.
func describeTrx(db *sql.DB) string {
	rows, err := db.Query(`SELECT t.trx_state, COALESCE(p.db, ''), COALESCE(t.trx_query, '')
		FROM information_schema.innodb_trx t
		LEFT JOIN information_schema.processlist p ON p.id = t.trx_mysql_thread_id`)
	if err != nil {
		return err.Error()
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var state, dbName, query string
		if err := rows.Scan(&state, &dbName, &query); err != nil {
			return err.Error()
		}
		out = append(out, state+" db="+dbName+" q="+query)
	}
	return strings.Join(out, "; ")
}
