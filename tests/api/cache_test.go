//go:build api

package api

import (
	"strings"
	"testing"

	"github.com/secunda/team-tasks-service/internal/testutil"
)

// Кеш списка через HTTP: эквивалентные запросы (регистр UUID, параметры по умолчанию) делят одну
// выборку, разные фильтры и страницы — нет; ошибки не кешируются; исключённый участник получает 404,
// а не сохранённый список.
func TestListCacheSelectionsAndAccess(t *testing.T) {
	c := newCrew(t)
	tk := c.mustCreate(c.owner, map[string]any{"title": "Первая", "assignee_id": c.bob.id})
	c.mustCreate(c.owner, map[string]any{"title": "Вторая"})
	keys := func() int { return len(testutil.CacheKeys(t, c.s.Redis, c.team)) }
	get := func(u user, query string) testutil.Response {
		return c.s.Do(testutil.Request{Method: "GET", Path: "/api/v1/tasks?" + query, Token: u.token})
	}

	expect(t, get(c.bob, "team_id="+c.team), 200, "")
	if keys() != 1 {
		t.Fatalf("ключей после первого списка: %d", keys())
	}
	// Та же выборка: регистр team_id, явные значения по умолчанию, другой участник команды.
	for _, q := range []string{
		"team_id=" + strings.ToUpper(c.team),
		"team_id=" + c.team + "&limit=20&offset=0&pagination=offset",
		"pagination=offset&team_id=" + c.team,
	} {
		expect(t, get(c.alice, q), 200, "")
	}
	if keys() != 1 {
		t.Fatalf("эквивалентные запросы создали новые выборки: %d", keys())
	}
	// assignee_id в разном регистре — одна выборка; другие фильтры, limit, offset и режим — разные.
	expect(t, get(c.bob, "team_id="+c.team+"&assignee_id="+c.bob.id), 200, "")
	expect(t, get(c.bob, "team_id="+c.team+"&assignee_id="+strings.ToUpper(c.bob.id)), 200, "")
	if keys() != 2 {
		t.Fatalf("регистр assignee_id разделил выборку: %d", keys())
	}
	for i, q := range []string{"&status=todo", "&limit=1", "&offset=1", "&pagination=cursor"} {
		expect(t, get(c.bob, "team_id="+c.team+q), 200, "")
		if keys() != 3+i {
			t.Fatalf("%s: ключей %d", q, keys())
		}
	}
	r := get(c.bob, "team_id="+c.team+"&pagination=cursor&limit=1")
	next := pagination(t, r)["next_cursor"].(string)
	expect(t, get(c.bob, "team_id="+c.team+"&pagination=cursor&limit=1&cursor="+next), 200, "")
	if keys() != 8 {
		t.Fatalf("страницы курсора: ключей %d", keys())
	}
	// Ошибки валидации и доступа не кешируются.
	expect(t, get(c.bob, "team_id="+c.team+"&status=closed"), 422, "validation_error")
	expect(t, get(c.outsider, "team_id="+c.team), 404, "not_found")
	if keys() != 8 {
		t.Fatalf("ошибка закеширована: %d", keys())
	}

	// Повтор выборки до изменений отдаёт тот же ответ, что и первый раз.
	first := get(c.bob, "team_id="+c.team)
	if string(first.Body) != string(get(c.alice, "team_id="+strings.ToUpper(c.team)).Body) {
		t.Fatal("ответы одной выборки различаются")
	}

	// Исключённый участник (DELETE — в группе 9, здесь напрямую) не получает кеш, который читал раньше.
	if _, err := c.s.DB.Exec(`DELETE FROM team_members WHERE team_id = ? AND user_id = ?`,
		idBytes(t, c.team), idBytes(t, c.bob.id)); err != nil {
		t.Fatal(err)
	}
	expect(t, get(c.bob, "team_id="+c.team), 404, "not_found")

	// Изменение задачи — следующий список учитывает его во всех выборках.
	expect(t, c.put(c.owner, tk["id"].(string), map[string]any{"version": 1, "status": "done"}), 200, "")
	todo := items(t, get(c.alice, "team_id="+c.team+"&status=todo"))
	if len(todo) != 1 || todo[0]["title"] != "Вторая" {
		t.Fatalf("todo после изменения: %v", todo)
	}
}
