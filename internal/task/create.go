package task

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/history"
)

// CreateInput — данные новой задачи. Пустые необязательные поля получают значения по умолчанию.
type CreateInput struct {
	TeamID      uuid.UUID
	Title       string
	Description string
	Status      string // "" — todo
	AssigneeID  *uuid.UUID
}

func (in CreateInput) validate() error {
	fields := map[string]string{}
	checkTitle(in.Title, fields)
	checkDescription(in.Description, fields)
	if in.Status != "" {
		checkStatus(in.Status, fields)
	}
	return fieldsError(fields)
}

// Create создаёт задачу и первую запись журнала в одной транзакции. Автор — текущий пользователь,
// version = 1; статус по умолчанию todo, создание сразу в done устанавливает closed_at.
func (s *Service) Create(ctx context.Context, actor uuid.UUID, in CreateInput) (Task, error) {
	if err := in.validate(); err != nil {
		return Task{}, err
	}
	status := StatusTodo
	if in.Status != "" {
		status = Status(in.Status)
	}
	// ID генерируются до транзакции: ошибка генерации не оставляет частичных записей.
	taskID, err := s.ids.NewID()
	if err != nil {
		return Task{}, fmt.Errorf("create task: %w", err)
	}
	historyID, err := s.ids.NewID()
	if err != nil {
		return Task{}, fmt.Errorf("create task: %w", err)
	}
	now := s.now()
	t := Task{
		ID: taskID, TeamID: in.TeamID, Title: in.Title, Description: in.Description, Status: status,
		CreatedBy: actor, AssigneeID: in.AssigneeID, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if status == StatusDone {
		t.ClosedAt = &now
	}
	changes, err := creationChanges(t).JSON()
	if err != nil {
		return Task{}, err
	}

	err = s.store.InTeamTx(ctx, in.TeamID, func(tx Tx) error {
		if _, member, err := tx.MemberRole(ctx, actor); err != nil {
			return err
		} else if !member {
			return ErrNotFound
		}
		if err := requireAssignee(ctx, tx, t.AssigneeID); err != nil {
			return err
		}
		if err := tx.InsertTask(ctx, t); err != nil {
			return err
		}
		if err := tx.InsertHistory(ctx, HistoryEntry{ID: historyID, TaskID: t.ID, ChangedBy: actor,
			TaskVersion: 1, Changes: changes, CreatedAt: now}); err != nil {
			return err
		}
		return tx.BumpListRevision(ctx)
	})
	if err != nil {
		return Task{}, err
	}
	return t, nil
}

// creationChanges — первая запись журнала: начальные значения с old = null.
func creationChanges(t Task) history.Changes {
	ch := history.Changes{
		history.Title:       {Old: nil, New: t.Title},
		history.Description: {Old: nil, New: t.Description},
		history.Status:      {Old: nil, New: string(t.Status)},
		history.CreatedBy:   {Old: nil, New: history.ID(&t.CreatedBy)},
		history.AssigneeID:  {Old: nil, New: history.ID(t.AssigneeID)},
	}
	if t.ClosedAt != nil {
		ch[history.ClosedAt] = history.Change{Old: nil, New: history.Time(t.ClosedAt)}
	}
	return ch
}
