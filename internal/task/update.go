package task

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/history"
)

// Field — поле частичного обновления в одном из трёх состояний: не передано (Set=false),
// явный null (Set && Null) или значение (Set && !Null). Указатель для этого недостаточен:
// он не отличает «ключа нет» от null.
type Field[T any] struct {
	Set   bool
	Null  bool
	Value T
}

// Set — поле передано со значением v.
func Set[T any](v T) Field[T] { return Field[T]{Set: true, Value: v} }

// Null — поле передано явным null.
func Null[T any]() Field[T] { return Field[T]{Set: true, Null: true} }

// Ptr возвращает указатель на значение или nil, если поле не передано или равно null.
func (f Field[T]) Ptr() *T {
	if !f.Set || f.Null {
		return nil
	}
	v := f.Value
	return &v
}

// UpdateInput — частичное обновление задачи. Null допустим только для AssigneeID (снять назначение).
// Version — обязательная клиентская версия: условие обновления, а не новое значение.
type UpdateInput struct {
	Title       Field[string]
	Description Field[string]
	Status      Field[string]
	AssigneeID  Field[uuid.UUID]
	Version     int64
}

// Fields перечисляет переданные изменяемые поля в стабильном порядке.
func (in UpdateInput) Fields() []string {
	var out []string
	if in.Title.Set {
		out = append(out, FieldTitle)
	}
	if in.Description.Set {
		out = append(out, FieldDescription)
	}
	if in.Status.Set {
		out = append(out, FieldStatus)
	}
	if in.AssigneeID.Set {
		out = append(out, FieldAssigneeID)
	}
	return out
}

// validate проверяет значения, не обращаясь к хранилищу; все ошибки сообщаются разом.
func (in UpdateInput) validate() error {
	fields := map[string]string{}
	if in.Version < 1 {
		fields["version"] = "обязательное положительное целое число"
	}
	if len(in.Fields()) == 0 {
		fields["body"] = "требуется хотя бы одно из полей title, description, status, assignee_id"
	}
	checkNonNull(FieldTitle, in.Title, checkTitle, fields)
	checkNonNull(FieldDescription, in.Description, checkDescription, fields)
	checkNonNull(FieldStatus, in.Status, checkStatus, fields)
	return fieldsError(fields)
}

// Update применяет частичное обновление с оптимистической блокировкой.
//
// Задача читается обычным SELECT, без SELECT ... FOR UPDATE. Совпадение версии в Go лишь даёт
// ранний 409: между этим чтением и записью другая транзакция может зафиксировать новую версию.
// Поэтому запись выполняет условный UPDATE ... WHERE version = <клиентская версия>, и только
// RowsAffected = 1 разрешает историю и ревизию кеша. RowsAffected = 0 — откат и 409 (или 404, если
// строка исчезла). Конфликт не повторяется автоматически и версия за клиента не подставляется:
// клиент заново читает задачу и осознанно повторяет правку.
func (s *Service) Update(ctx context.Context, actor, taskID uuid.UUID, in UpdateInput) (Task, error) {
	if err := in.validate(); err != nil {
		return Task{}, err
	}
	teamID, err := s.store.TaskTeam(ctx, taskID)
	if err != nil {
		return Task{}, err
	}
	historyID, err := s.ids.NewID()
	if err != nil {
		return Task{}, fmt.Errorf("update task: %w", err)
	}

	var result Task
	err = s.store.InTeamTx(ctx, teamID, func(tx Tx) error {
		role, member, err := tx.MemberRole(ctx, actor)
		if err != nil {
			return err
		}
		if !member {
			return ErrNotFound
		}
		current, found, err := tx.Task(ctx, taskID)
		if err != nil {
			return err
		}
		if !found {
			return ErrNotFound
		}
		// Права проверяются до раскрытия текущей версии.
		if !CanUpdateFields(role, actor, current, in.Fields()) {
			return ErrForbidden
		}
		// Проверка версии предшествует no-op: устаревшая версия — 409 даже при совпадающих значениях.
		if in.Version != current.Version {
			return &VersionConflictError{Expected: in.Version, Current: current.Version}
		}
		if in.AssigneeID.Set {
			if err := requireAssignee(ctx, tx, in.AssigneeID.Ptr()); err != nil {
				return err
			}
		}
		now := s.now()
		next, changes := apply(current, in, now)
		if len(changes) == 0 {
			result = current // no-op: без новой версии, updated_at, истории и ревизии кеша
			return nil
		}
		next.Version = in.Version + 1
		next.UpdatedAt = now

		ok, err := tx.UpdateIfVersion(ctx, next, in.Version)
		if err != nil {
			return err
		}
		if !ok {
			// Между чтением и UPDATE версию изменила другая транзакция (или строка исчезла).
			v, exists, err := tx.Version(ctx, taskID)
			if err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
			return &VersionConflictError{Expected: in.Version, Current: v}
		}
		raw, err := changes.JSON()
		if err != nil {
			return err
		}
		if err := tx.InsertHistory(ctx, HistoryEntry{ID: historyID, TaskID: taskID, ChangedBy: actor,
			TaskVersion: next.Version, Changes: raw, CreatedAt: now}); err != nil {
			return err
		}
		if err := tx.BumpListRevision(ctx); err != nil {
			return err
		}
		result = next
		return nil
	})
	if err != nil {
		return Task{}, err
	}
	return result, nil
}

// apply применяет переданные поля к прочитанному состоянию и возвращает новое состояние и
// фактические изменения, включая автоматический closed_at. Пустой diff означает no-op.
// Вход в done ставит closed_at = now, выход очищает; done → done дату закрытия не сдвигает.
// Вход уже прошёл validate: null приходит только в AssigneeID.
func apply(t Task, in UpdateInput, now time.Time) (Task, history.Changes) {
	next, ch := t, history.Changes{}
	if in.Title.Set && in.Title.Value != t.Title {
		next.Title = in.Title.Value
		ch[history.Title] = history.Change{Old: t.Title, New: next.Title}
	}
	if in.Description.Set && in.Description.Value != t.Description {
		next.Description = in.Description.Value
		ch[history.Description] = history.Change{Old: t.Description, New: next.Description}
	}
	if in.Status.Set && Status(in.Status.Value) != t.Status {
		next.Status = Status(in.Status.Value)
		ch[history.Status] = history.Change{Old: string(t.Status), New: string(next.Status)}
		switch {
		case next.Status == StatusDone:
			closed := now
			next.ClosedAt = &closed
		case t.Status == StatusDone:
			next.ClosedAt = nil
		}
		if !sameTime(t.ClosedAt, next.ClosedAt) {
			ch[history.ClosedAt] = history.Change{Old: history.Time(t.ClosedAt), New: history.Time(next.ClosedAt)}
		}
	}
	if assignee := in.AssigneeID.Ptr(); in.AssigneeID.Set && !sameID(t.AssigneeID, assignee) {
		next.AssigneeID = assignee
		ch[history.AssigneeID] = history.Change{Old: history.ID(t.AssigneeID), New: history.ID(next.AssigneeID)}
	}
	return next, ch
}

func sameID(a, b *uuid.UUID) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}
