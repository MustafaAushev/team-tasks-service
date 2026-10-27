package task

import (
	"context"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
)

// HistoryPage — страница журнала; NextCursor == nil в конце.
type HistoryPage struct {
	Items      []HistoryEntry
	Limit      int
	NextCursor *string
}

// History возвращает журнал задачи участнику её команды: порядок task_version ASC, только курсор.
func (s *Service) History(ctx context.Context, actor, taskID uuid.UUID, limit int, token string) (HistoryPage, error) {
	fields := map[string]string{}
	checkLimit(limit, fields)
	if err := fieldsError(fields); err != nil {
		return HistoryPage{}, err
	}
	t, err := s.store.VisibleTask(ctx, taskID, actor)
	if err != nil {
		return HistoryPage{}, err
	}
	want := cursor{Kind: kindHistory, TeamID: platform.IDString(t.TeamID), TaskID: platform.IDString(t.ID)}
	var after int64
	if token != "" {
		c, err := s.cursors.decode(token, want)
		if err != nil {
			return HistoryPage{}, err
		}
		if c.LastVer < 1 {
			return HistoryPage{}, ErrInvalidCursor
		}
		after = c.LastVer
	}
	items, err := s.store.History(ctx, taskID, after, limit+1)
	if err != nil {
		return HistoryPage{}, err
	}
	page := HistoryPage{Limit: limit}
	if len(items) > limit {
		items = items[:limit]
		want.LastVer = items[len(items)-1].TaskVersion
		next := s.cursors.encode(want)
		page.NextCursor = &next
	}
	page.Items = items
	return page, nil
}
