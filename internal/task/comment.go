package task

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
)

// CommentPage — страница комментариев; NextCursor == nil в конце.
type CommentPage struct {
	Items      []Comment
	Limit      int
	NextCursor *string
}

// AddComment добавляет комментарий от имени текущего участника. Проверка членства синхронизирована
// с изменениями состава общей блокировкой команды; поля, версия и журнал задачи не меняются.
func (s *Service) AddComment(ctx context.Context, actor, taskID uuid.UUID, content string) (Comment, error) {
	fields := map[string]string{}
	checkComment(content, fields)
	if err := fieldsError(fields); err != nil {
		return Comment{}, err
	}
	teamID, err := s.store.TaskTeam(ctx, taskID)
	if err != nil {
		return Comment{}, err
	}
	id, err := s.ids.NewID()
	if err != nil {
		return Comment{}, fmt.Errorf("add comment: %w", err)
	}
	c := Comment{ID: id, TaskID: taskID, UserID: actor, Content: content, CreatedAt: s.now()}
	err = s.store.InTeamTx(ctx, teamID, func(tx Tx) error {
		if _, member, err := tx.MemberRole(ctx, actor); err != nil {
			return err
		} else if !member {
			return ErrNotFound
		}
		return tx.InsertComment(ctx, c)
	})
	if err != nil {
		return Comment{}, err
	}
	return c, nil
}

// Comments возвращает комментарии участнику команды задачи: порядок id ASC, только курсор.
func (s *Service) Comments(ctx context.Context, actor, taskID uuid.UUID, limit int, token string) (CommentPage, error) {
	fields := map[string]string{}
	checkLimit(limit, fields)
	if err := fieldsError(fields); err != nil {
		return CommentPage{}, err
	}
	t, err := s.store.VisibleTask(ctx, taskID, actor)
	if err != nil {
		return CommentPage{}, err
	}
	want := cursor{Kind: kindComments, TeamID: platform.IDString(t.TeamID), TaskID: platform.IDString(t.ID)}
	var after *uuid.UUID
	if token != "" {
		c, err := s.cursors.decode(token, want)
		if err != nil {
			return CommentPage{}, err
		}
		id, err := platform.ParseID(c.LastID)
		if err != nil {
			return CommentPage{}, ErrInvalidCursor
		}
		after = &id
	}
	items, err := s.store.Comments(ctx, taskID, after, limit+1)
	if err != nil {
		return CommentPage{}, err
	}
	page := CommentPage{Limit: limit}
	if len(items) > limit {
		items = items[:limit]
		want.LastID = platform.IDString(items[len(items)-1].ID)
		next := s.cursors.encode(want)
		page.NextCursor = &next
	}
	page.Items = items
	return page, nil
}
