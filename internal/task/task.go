// Package task содержит бизнес-правила задач: права на поля, частичное обновление с
// оптимистической блокировкой по клиентской версии, статусы и closed_at, журнал изменений,
// комментарии и курсорную пагинацию.
package task

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Status — статус задачи. Набор фиксирован; любые переходы допустимы при наличии права.
type Status string

const (
	StatusTodo       Status = "todo"
	StatusInProgress Status = "in_progress"
	StatusDone       Status = "done"
)

// ParseStatus принимает только известные статусы.
func ParseStatus(s string) (Status, bool) {
	switch st := Status(s); st {
	case StatusTodo, StatusInProgress, StatusDone:
		return st, true
	}
	return "", false
}

// Task — задача. AssigneeID и ClosedAt — nil, когда значения нет.
type Task struct {
	ID          uuid.UUID
	TeamID      uuid.UUID
	Title       string
	Description string
	Status      Status
	CreatedBy   uuid.UUID
	AssigneeID  *uuid.UUID
	Version     int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ClosedAt    *time.Time
}

// HistoryEntry — неизменяемая запись журнала: одна на каждую ревизию задачи.
// Changes — JSON-объект {"поле": {"old": ..., "new": ...}}; ID внутри — UUID-строки или null.
type HistoryEntry struct {
	ID          uuid.UUID
	TaskID      uuid.UUID
	ChangedBy   uuid.UUID
	TaskVersion int64
	Changes     json.RawMessage
	CreatedAt   time.Time
}

// Comment — комментарий к задаче; не меняет поля, версию и журнал задачи.
type Comment struct {
	ID        uuid.UUID
	TaskID    uuid.UUID
	UserID    uuid.UUID
	Content   string
	CreatedAt time.Time
}

var (
	// ErrNotFound — задачи или команды нет либо пользователь в команде не состоит.
	ErrNotFound = errors.New("task: not found")
	// ErrForbidden — участник команды не вправе изменить хотя бы одно из переданных полей.
	ErrForbidden = errors.New("task: forbidden")
	// ErrAssigneeNotInTeam — исполнитель не состоит в команде задачи.
	ErrAssigneeNotInTeam = errors.New("task: assignee is not a team member")
	// ErrInvalidCursor — курсор повреждён, подписан не нами или относится к другой выборке.
	ErrInvalidCursor = errors.New("task: invalid cursor")
)

// VersionConflictError — клиентская версия не совпала с текущей (HTTP 409 task_version_conflict).
type VersionConflictError struct {
	Expected int64 // версия из запроса клиента
	Current  int64 // актуальная версия задачи
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("task: version conflict: expected %d, current %d", e.Expected, e.Current)
}
