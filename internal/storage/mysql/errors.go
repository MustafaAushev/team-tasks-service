package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"

	"github.com/go-sql-driver/mysql"
)

// Коды MySQL, после которых транзакция откатилась по технической причине и её можно повторить целиком.
const (
	errLockWaitTimeout = 1205
	errDeadlock        = 1213
)

// IsUnavailable — технический отказ MySQL или исчерпанный бюджет: клиенту 503, а не 500 и не 409.
// Блокировки, исчерпавшие повторы, обрыв или отказ соединения, остановка сервера, тайм-аут запроса.
func IsUnavailable(err error) bool {
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		switch me.Number {
		case errLockWaitTimeout, errDeadlock,
			1040, // too many connections
			1053, // server shutdown in progress
			3024: // max_execution_time exceeded
			return true
		}
		return false
	}
	var ne net.Error
	return errors.As(err, &ne) ||
		errors.Is(err, driver.ErrBadConn) || errors.Is(err, mysql.ErrInvalidConn) || errors.Is(err, sql.ErrConnDone) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// retryable — транзакция откатилась из-за взаимоблокировки или ожидания блокировки и может быть
// выполнена заново с теми же входными данными (в том числе исходной клиентской version). Ошибка
// commit сюда не относится: исход commit неизвестен, повтор мог бы применить изменение дважды.
func retryable(err error) bool {
	var ce *commitError
	if errors.As(err, &ce) {
		return false
	}
	var me *mysql.MySQLError
	return errors.As(err, &me) && (me.Number == errLockWaitTimeout || me.Number == errDeadlock)
}

// commitError помечает ошибку commit: её исход неизвестен, транзакция не повторяется.
type commitError struct{ err error }

func (e *commitError) Error() string { return "commit: " + e.err.Error() }
func (e *commitError) Unwrap() error { return e.err }
