// Package history задаёт формат журнала изменений задачи: JSON-объект {"поле": {"old": ..., "new": ...}}.
// Журнал пишут сценарии задач и исключение участника, снимающее его назначения; формат у них общий.
package history

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
)

// Ключи журнала — имена полей задачи.
const (
	Title       = "title"
	Description = "description"
	Status      = "status"
	AssigneeID  = "assignee_id"
	CreatedBy   = "created_by"
	ClosedAt    = "closed_at"
)

// Change — пара старого и нового значения поля.
type Change struct {
	Old any `json:"old"`
	New any `json:"new"`
}

// Changes — изменения одной ревизии задачи; ключи — имена полей.
type Changes map[string]Change

// JSON сериализует изменения для колонки task_history.changes.
func (c Changes) JSON() (json.RawMessage, error) { return json.Marshal(c) }

// ID — значение-идентификатор: UUID-строка в нижнем регистре или nil (JSON null).
func ID(id *uuid.UUID) any {
	if id == nil {
		return nil
	}
	return platform.IDString(*id)
}

// Time — значение-время: RFC 3339 в UTC или nil (JSON null).
func Time(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}
