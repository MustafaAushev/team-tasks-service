package httpapi

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"net/http/httptest"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/secunda/team-tasks-service/internal/platform"
)

// Технические отказы MySQL и исчерпанный бюджет — 503 dependency_unavailable (не 500 и не 409);
// прочие непредвиденные ошибки — 500. Текст ошибки клиенту не передаётся.
func TestDependencyErrorsAre503(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/v1/teams", nil)
	wrap := func(err error) error { return fmt.Errorf("update task: %w", err) }
	for name, c := range map[string]struct {
		err    error
		status int
	}{
		"lock wait timeout": {wrap(&mysql.MySQLError{Number: 1205, Message: "Lock wait timeout exceeded"}), 503},
		"deadlock":          {wrap(&mysql.MySQLError{Number: 1213, Message: "Deadlock found"}), 503},
		"too many conns":    {&mysql.MySQLError{Number: 1040}, 503},
		"обрыв соединения":  {wrap(mysql.ErrInvalidConn), 503},
		"bad conn":          {driver.ErrBadConn, 503},
		"сеть":              {wrap(&net.OpError{Op: "dial", Err: errors.New("connection refused")}), 503},
		"тайм-аут запроса":  {wrap(context.DeadlineExceeded), 503},
		"отмена":            {context.Canceled, 503},
		"дубликат":          {&mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}, 500},
		"прочее":            {errors.New("boom"), 500},
	} {
		e := unexpectedError(platform.DiscardLogger(), r, c.err)
		if e.Status != c.status {
			t.Errorf("%s: %d, ожидалось %d", name, e.Status, c.status)
		}
		if c.status == 503 && e.Code != "dependency_unavailable" {
			t.Errorf("%s: код %s", name, e.Code)
		}
	}
}
