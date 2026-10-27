package httpapi

import (
	"bytes"
	"encoding/json"
	"strconv"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/task"
)

// updatableKeys — единственные допустимые ключи тела; всё остальное (включая серверные
// атрибуты id, team_id, created_by, created_at, updated_at, closed_at) отклоняется.
var updatableKeys = map[string]bool{
	"title": true, "description": true, "status": true, "assignee_id": true, "version": true,
}

// parseUpdateTask разбирает тело PUT /tasks/{id} в task.UpdateInput, различая отсутствие, null и
// значение каждого поля. Проверяется только форма запроса: JSON-объект, известные ключи, типы
// значений, version — целочисленный литерал. Обязательность и положительность version, «хотя бы
// одно поле», недопустимость null, длины и статусы проверяет task.UpdateInput.
func parseUpdateTask(body []byte) (task.UpdateInput, *apiError) {
	var in task.UpdateInput
	if len(bytes.TrimSpace(body)) == 0 {
		return in, errInvalidJSON("Тело запроса пусто")
	}
	if !json.Valid(body) {
		return in, errInvalidJSON("Некорректный JSON")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil || raw == nil {
		return in, errField("body", "ожидается JSON-объект")
	}

	fields := map[string]string{}
	for key := range raw {
		if !updatableKeys[key] {
			fields[key] = "неизвестное или недоступное для изменения поле"
		}
	}

	in.Title = stringField(raw, "title", fields)
	in.Description = stringField(raw, "description", fields)
	in.Status = stringField(raw, "status", fields)
	in.AssigneeID = assigneeField(raw, fields)
	if v, ok := raw["version"]; ok {
		in.Version = intField(v, "version", fields)
	}

	if len(fields) > 0 {
		return task.UpdateInput{}, errValidation(fields)
	}
	return in, nil
}

// stringField: строка или null.
func stringField(raw map[string]json.RawMessage, key string, errs map[string]string) task.Field[string] {
	v, ok := raw[key]
	if !ok {
		return task.Field[string]{}
	}
	if isNull(v) {
		return task.Null[string]()
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		errs[key] = "ожидается строка"
		return task.Field[string]{}
	}
	return task.Set(s)
}

// assigneeField: UUIDv7-строка или null (снять назначение).
func assigneeField(raw map[string]json.RawMessage, errs map[string]string) task.Field[uuid.UUID] {
	v, ok := raw["assignee_id"]
	if !ok {
		return task.Field[uuid.UUID]{}
	}
	if isNull(v) {
		return task.Null[uuid.UUID]()
	}
	var s string
	if err := json.Unmarshal(v, &s); err != nil {
		errs["assignee_id"] = "ожидается UUID версии 7 или null"
		return task.Field[uuid.UUID]{}
	}
	id, err := platform.ParseID(s)
	if err != nil {
		errs["assignee_id"] = "ожидается UUID версии 7 или null"
		return task.Field[uuid.UUID]{}
	}
	return task.Set(id)
}

func isNull(v json.RawMessage) bool { return string(bytes.TrimSpace(v)) == "null" }

// intField принимает только целочисленный литерал в пределах int64: строки, дроби, экспонента,
// null и другие типы отклоняются. Знак не проверяется — это правило домена.
func intField(v json.RawMessage, key string, errs map[string]string) int64 {
	n, err := strconv.ParseInt(string(bytes.TrimSpace(v)), 10, 64)
	if err != nil {
		errs[key] = "ожидается целое число"
		return 0
	}
	return n
}
