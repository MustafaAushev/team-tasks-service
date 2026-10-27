package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
)

// Filter — выборка списка задач команды; фильтры объединяются по И.
type Filter struct {
	TeamID     uuid.UUID
	Status     *Status
	AssigneeID *uuid.UUID
}

// Режимы пагинации списка задач.
const (
	PageOffset = "offset"
	PageCursor = "cursor"
)

// ListQuery — запрос списка задач. Mode "" означает offset.
type ListQuery struct {
	TeamID     uuid.UUID
	Status     string
	AssigneeID *uuid.UUID
	Mode       string
	Limit      int
	Offset     *int // nil — параметр не передан
	Cursor     string
}

// Page — страница задач. Total заполнен только в режиме offset.
type Page struct {
	Items      []Task
	Mode       string
	Limit      int
	Offset     int
	Total      int
	NextCursor *string
}

// ListCache — кеш страниц списка задач (cache-aside). Сбои кеша не возвращаются: Get при любой
// проблеме даёт промах, Set ничего не гарантирует. Источник истины — MySQL.
type ListCache interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	Set(ctx context.Context, key string, value []byte)
}

// List возвращает страницу задач команды в режиме offset (по умолчанию) или cursor.
func (s *Service) List(ctx context.Context, actor uuid.UUID, q ListQuery) (Page, error) {
	fields := map[string]string{}
	if q.Status != "" {
		checkStatus(q.Status, fields)
	}
	checkPagination(q.Mode, q.Limit, q.Offset, q.Cursor, fields)
	if err := fieldsError(fields); err != nil {
		return Page{}, err
	}
	f := Filter{TeamID: q.TeamID, AssigneeID: q.AssigneeID}

	if q.Status != "" {
		st := Status(q.Status)
		f.Status = &st
	}

	// Нормализованная выборка: регистр UUID и параметры по умолчанию не меняют ключ кеша.
	sel := selection{status: q.Status, assignee: idString(f.AssigneeID), limit: q.Limit, mode: PageOffset}
	want := cursor{Kind: kindTasks, TeamID: platform.IDString(f.TeamID), Status: q.Status, Assignee: idString(f.AssigneeID)}
	var before *uuid.UUID
	if q.Mode == PageCursor {
		sel.mode = PageCursor
		if q.Cursor != "" {
			c, err := s.cursors.decode(q.Cursor, want)
			if err != nil {
				return Page{}, err
			}
			id, err := platform.ParseID(c.LastID)
			if err != nil {
				return Page{}, ErrInvalidCursor
			}
			before = &id
			sel.before = platform.IDString(id)
		}
	} else if q.Offset != nil {
		sel.offset = *q.Offset
	}

	// Членство и ревизия читаются из MySQL до Redis: исключённый участник не получит кеш, а запись,
	// сделанная до последнего изменения задач команды, лежит под старой ревизией и не используется.
	if s.cache != nil {
		rev, err := s.store.ListRevision(ctx, f.TeamID, actor)
		if err != nil {
			return Page{}, err
		}
		if raw, ok := s.cache.Get(ctx, listKey(f.TeamID, rev, sel)); ok {
			var page Page
			if json.Unmarshal(raw, &page) == nil {
				return page, nil
			}
		}
	}

	page, rev, err := s.readPage(ctx, actor, f, sel, before, want)
	if err != nil {
		return Page{}, err
	}
	// Страница кешируется под ревизией того же SQL-снимка, из которого прочитана.
	if s.cache != nil {
		if raw, err := json.Marshal(page); err == nil {
			s.cache.Set(ctx, listKey(f.TeamID, rev, sel), raw)
		}
	}
	return page, nil
}

// readPage читает страницу из MySQL и возвращает ревизию списков из того же снимка.
func (s *Service) readPage(ctx context.Context, actor uuid.UUID, f Filter, sel selection, before *uuid.UUID, want cursor) (Page, int64, error) {
	if sel.mode == PageOffset {
		items, total, rev, err := s.store.ListOffset(ctx, actor, f, sel.limit, sel.offset)
		if err != nil {
			return Page{}, 0, err
		}
		return Page{Items: items, Mode: PageOffset, Limit: sel.limit, Offset: sel.offset, Total: total}, rev, nil
	}
	items, rev, err := s.store.ListBefore(ctx, actor, f, before, sel.limit+1)
	if err != nil {
		return Page{}, 0, err
	}
	page := Page{Mode: PageCursor, Limit: sel.limit}
	if len(items) > sel.limit {
		items = items[:sel.limit]
		want.LastID = platform.IDString(items[len(items)-1].ID)
		next := s.cursors.encode(want)
		page.NextCursor = &next
	}
	page.Items = items
	return page, rev, nil
}

// selection — нормализованные параметры выборки списка; before — позиция из проверенного курсора.
type selection struct {
	status, assignee, mode, before string
	limit, offset                  int
}

// listKey — ключ кеша tasks:v1:{team_id}:{revision}:{hash(выборки)}. Новая ревизия команды делает
// все прежние ключи недоступными; они не удаляются, а истекают по TTL или вытесняются.
//
// ВНИМАНИЕ: значение — json.Marshal(Page) без json-тегов, то есть ключи JSON — имена полей Page и Task.
// При переименовании, удалении или смене типа их полей поднимите версию v1 → v2: иначе после деплоя
// json.Unmarshal молча прочитает старые записи с пустыми полями (неизвестные ключи игнорируются),
// и до конца TTL клиенты получат неполные задачи.
func listKey(teamID uuid.UUID, revision int64, sel selection) string {
	raw := "status=" + sel.status + "&assignee=" + sel.assignee + "&mode=" + sel.mode +
		"&limit=" + strconv.Itoa(sel.limit) + "&offset=" + strconv.Itoa(sel.offset) + "&before=" + sel.before
	sum := sha256.Sum256([]byte(raw))
	return "tasks:v1:" + platform.IDString(teamID) + ":" + strconv.FormatInt(revision, 10) + ":" + hex.EncodeToString(sum[:])
}

func idString(id *uuid.UUID) string {
	if id == nil {
		return ""
	}
	return platform.IDString(*id)
}
