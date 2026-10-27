//go:build integration

package integration

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/task"
	"github.com/secunda/team-tasks-service/internal/team"
	"github.com/secunda/team-tasks-service/internal/testutil"
)

// История: порядок task_version ASC, страницы по task_version > last_version, конец — nil.
func TestHistoryCursorPagination(t *testing.T) {
	e := newEnv(t)
	owner, outsider := e.user("O"), e.user("X")
	tm := e.teamWith(owner, nil)
	tk := e.createTask(owner, tm, "v1", nil)
	for v := int64(1); v < 5; v++ {
		if _, err := e.tasks.Update(e.ctx, owner, tk.ID, task.UpdateInput{Version: v, Title: task.Set("v" + string(rune('1'+v)))}); err != nil {
			t.Fatal(err)
		}
	}

	var versions []int64
	page, err := e.tasks.History(e.ctx, owner, tk.ID, 2, "")
	for {
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range page.Items {
			versions = append(versions, h.TaskVersion)
		}
		if page.NextCursor == nil {
			break
		}
		page, err = e.tasks.History(e.ctx, owner, tk.ID, 2, *page.NextCursor)
	}
	if !slices.Equal(versions, []int64{1, 2, 3, 4, 5}) {
		t.Fatalf("ревизии: %v", versions)
	}

	// Ровно на границе: последняя полная страница не обещает продолжения.
	if p, _ := e.tasks.History(e.ctx, owner, tk.ID, 5, ""); p.NextCursor != nil {
		t.Fatal("next_cursor должен быть nil, когда записей больше нет")
	}
	// Неучастник — ErrNotFound; курсор другой задачи или вида списка — ErrInvalidCursor.
	if _, err := e.tasks.History(e.ctx, outsider, tk.ID, 2, ""); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("неучастник: %v", err)
	}
	first, _ := e.tasks.History(e.ctx, owner, tk.ID, 2, "")
	other := e.createTask(owner, tm, "Другая", nil)
	if _, err := e.tasks.History(e.ctx, owner, other.ID, 2, *first.NextCursor); !errors.Is(err, task.ErrInvalidCursor) {
		t.Fatalf("курсор другой задачи: %v", err)
	}
	if _, err := e.tasks.AddComment(e.ctx, owner, tk.ID, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.tasks.AddComment(e.ctx, owner, tk.ID, "b"); err != nil {
		t.Fatal(err)
	}
	comments, _ := e.tasks.Comments(e.ctx, owner, tk.ID, 1, "")
	if _, err := e.tasks.History(e.ctx, owner, tk.ID, 2, *comments.NextCursor); !errors.Is(err, task.ErrInvalidCursor) {
		t.Fatalf("курсор комментариев в истории: %v", err)
	}
	if _, err := e.tasks.Comments(e.ctx, owner, tk.ID, 2, *first.NextCursor); !errors.Is(err, task.ErrInvalidCursor) {
		t.Fatalf("курсор истории в комментариях: %v", err)
	}
}

// Комментарии: UUIDv7 одной временной части, created_at не по порядку, перемешанная вставка —
// страницы по id > last_id читают всё ровно один раз; добавленный между страницами комментарий
// с бо́льшим ID появляется в конце без повторов.
func TestCommentCursorPaginationStableOrder(t *testing.T) {
	e := newEnv(t)
	owner, member := e.user("O"), e.user("M")
	tm := e.teamWith(owner, map[uuid.UUID]team.Role{member: team.RoleMember})
	tk := e.createTask(owner, tm, "T", nil)

	ids := testutil.SameMillisV7(t, e.clock.Now().UnixMilli()+1, 7)
	base := e.clock.Now()
	for i, id := range ids {
		e.ids.Push(id)
		e.clock.Set(base.Add(-time.Duration(i) * time.Minute))
		if _, err := e.tasks.AddComment(e.ctx, member, tk.ID, "c"); err != nil {
			t.Fatal(err)
		}
	}
	want := sortedDesc(ids)
	slices.Reverse(want) // id ASC

	var seen []uuid.UUID
	page, err := e.tasks.Comments(e.ctx, owner, tk.ID, 3, "")
	if err != nil {
		t.Fatal(err)
	}
	// Комментарий с заведомо бо́льшим ID после первой страницы.
	later, err := e.tasks.AddComment(e.ctx, owner, tk.ID, "позже")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, later.ID)
	for {
		for _, c := range page.Items {
			if c.UserID == uuid.Nil || c.Content == "" {
				t.Fatalf("комментарий без автора или текста: %+v", c)
			}
			seen = append(seen, c.ID)
		}
		if page.NextCursor == nil {
			break
		}
		if page, err = e.tasks.Comments(e.ctx, owner, tk.ID, 3, *page.NextCursor); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(seen, want) {
		t.Fatalf("порядок или полнота нарушены:\n%v\n%v", seen, want)
	}
}

func TestCommentsAreForMembersAndKeepTaskVersion(t *testing.T) {
	e := newEnv(t)
	owner, member, outsider := e.user("O"), e.user("M"), e.user("X")
	tm := e.teamWith(owner, map[uuid.UUID]team.Role{member: team.RoleMember})
	tk := e.createTask(owner, tm, "T", nil)
	rev := e.revision(tm)

	// Member без прав на редактирование комментирует от своего имени; версия, история и кеш не меняются.
	c, err := e.tasks.AddComment(e.ctx, member, tk.ID, "  Проверил  ")
	if err != nil || c.UserID != member || c.Content != "  Проверил  " {
		t.Fatalf("комментарий: %+v %v", c, err)
	}
	if e.version(tk.ID) != 1 || e.historyCount(tk.ID) != 1 || e.revision(tm) != rev {
		t.Fatal("комментарий изменил версию, историю или ревизию кеша")
	}
	for name, err := range map[string]error{
		"неучастник":   func() error { _, err := e.tasks.AddComment(e.ctx, outsider, tk.ID, "x"); return err }(),
		"нет задачи":   func() error { _, err := e.tasks.AddComment(e.ctx, owner, mustID(t), "x"); return err }(),
		"чтение чужим": func() error { _, err := e.tasks.Comments(e.ctx, outsider, tk.ID, 20, ""); return err }(),
	} {
		if !errors.Is(err, task.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	var ve *platform.ValidationError
	if _, err := e.tasks.AddComment(e.ctx, member, tk.ID, "   "); !errors.As(err, &ve) {
		t.Fatalf("пустой комментарий: %v", err)
	}

	// Автор исключён (здесь — напрямую, сценарий DELETE реализует группа 9): комментарий остаётся
	// с прежним авторством, а сам автор теряет доступ к обсуждению.
	mustExecDB(t, e.db, `DELETE FROM team_members WHERE team_id = ? AND user_id = ?`, platform.IDBytes(tm), platform.IDBytes(member))
	page, err := e.tasks.Comments(e.ctx, owner, tk.ID, 20, "")
	if err != nil || len(page.Items) != 1 || page.Items[0].UserID != member {
		t.Fatalf("комментарий исключённого автора: %+v %v", page, err)
	}
	if _, err := e.tasks.Comments(e.ctx, member, tk.ID, 20, ""); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("исключённый автор: %v", err)
	}
	if _, err := e.tasks.AddComment(e.ctx, member, tk.ID, "ещё"); !errors.Is(err, task.ErrNotFound) {
		t.Fatalf("исключённый автор пишет: %v", err)
	}
}
