//go:build api

package api

import (
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/testutil"
)

// pages обходит курсорный список целиком; клиент передаёт next_cursor как есть, без разбора.
// Сервер курсоры не хранит: каждая страница восстанавливает позицию только из токена.
func pages(t *testing.T, c *crew, u user, path string, limit int, onPage func(n int)) []map[string]any {
	t.Helper()
	var all []map[string]any
	query := url.Values{"limit": {strconv.Itoa(limit)}}
	for n := 1; ; n++ {
		r := c.s.Do(testutil.Request{Method: "GET", Path: path + "?" + query.Encode(), Token: u.token})
		expect(t, r, 200, "")
		page := items(t, r)
		p := pagination(t, r)
		if p["mode"] != "cursor" || p["limit"] != float64(limit) {
			t.Fatalf("pagination: %v", p)
		}
		if len(page) > limit {
			t.Fatalf("страница больше limit: %d", len(page))
		}
		all = append(all, page...)
		if onPage != nil {
			onPage(n)
		}
		next, _ := p["next_cursor"].(string)
		if p["next_cursor"] == nil {
			return all
		}
		query.Set("cursor", next)
	}
}

func TestTaskHistoryHTTP(t *testing.T) {
	c := newCrew(t)
	tk := c.mustCreate(c.alice, map[string]any{"title": "v1", "assignee_id": c.bob.id})
	id := tk["id"].(string)
	expect(t, c.put(c.alice, id, map[string]any{"version": 1, "title": "v2", "assignee_id": nil}), 200, "")
	expect(t, c.put(c.bob, id, map[string]any{"version": 2, "status": "done"}), 403, "forbidden") // уже не исполнитель
	expect(t, c.put(c.owner, id, map[string]any{"version": 1, "status": "done"}), 409, "task_version_conflict")
	expect(t, c.put(c.owner, id, map[string]any{"version": 2, "status": "done"}), 200, "")

	path := "/api/v1/tasks/" + id + "/history"
	// Первая, следующая и последняя страницы; запись, добавленная между страницами, читается в конце.
	all := pages(t, c, c.bob, path, 1, func(n int) {
		if n == 1 {
			expect(t, c.put(c.alice, id, map[string]any{"version": 3, "description": "между страницами"}), 200, "")
		}
	})
	if len(all) != 4 {
		t.Fatalf("записей %d, ожидалось 4: %v", len(all), all)
	}
	for i, h := range all {
		if h["task_version"] != float64(i+1) || h["task_id"] != id {
			t.Fatalf("позиция %d: %v", i, h)
		}
	}
	// Начальное событие: old = null, автор — создатель; ID внутри changes — строки.
	created := all[0]["changes"].(map[string]any)
	if all[0]["changed_by"] != c.alice.id || created["title"].(map[string]any)["old"] != nil ||
		created["assignee_id"].(map[string]any)["new"] != c.bob.id || created["created_by"].(map[string]any)["new"] != c.alice.id {
		t.Fatalf("событие создания: %v", all[0])
	}
	// Журнал с несколькими полями: title и снятие исполнителя в одной ревизии.
	second := all[1]["changes"].(map[string]any)
	if len(second) != 2 || second["assignee_id"].(map[string]any)["old"] != c.bob.id || second["assignee_id"].(map[string]any)["new"] != nil {
		t.Fatalf("ревизия 2: %v", second)
	}
	// Закрытие пишет status и closed_at; 409 и 403 событий не создали.
	if third := all[2]["changes"].(map[string]any); third["closed_at"] == nil || all[2]["changed_by"] != c.owner.id {
		t.Fatalf("ревизия 3: %v", all[2])
	}

	// Сервер не хранит курсоры: страница, начатая одним экземпляром приложения, продолжается другим,
	// у которого нет никакой памяти о выданном токене.
	fork := c.s.Fork()
	r0 := c.s.Do(testutil.Request{Method: "GET", Path: path + "?limit=2", Token: c.bob.token})
	next := pagination(t, r0)["next_cursor"].(string)
	r1 := fork.Do(testutil.Request{Method: "GET", Path: path + "?limit=2&cursor=" + url.QueryEscape(next), Token: c.bob.token})
	expect(t, r1, 200, "")
	if got := items(t, r1); len(got) != 2 || got[0]["task_version"] != float64(3) {
		t.Fatalf("продолжение другим экземпляром: %s", r1.Body)
	}

	// Последняя полная страница: next_cursor = null.
	r := c.s.Do(testutil.Request{Method: "GET", Path: path + "?limit=4", Token: c.bob.token})
	if pagination(t, r)["next_cursor"] != nil {
		t.Fatalf("конец списка: %s", r.Body)
	}
	// Значение limit по умолчанию — 20.
	if p := pagination(t, c.s.Do(testutil.Request{Method: "GET", Path: path, Token: c.bob.token})); p["limit"] != float64(20) {
		t.Fatalf("limit по умолчанию: %v", p)
	}

	first := pagination(t, c.s.Do(testutil.Request{Method: "GET", Path: path + "?limit=1", Token: c.bob.token}))["next_cursor"].(string)
	other := c.mustCreate(c.owner, map[string]any{"title": "Другая"})["id"].(string)
	for name, req := range map[string]struct {
		path   string
		status int
		code   string
	}{
		"неучастник":            {path, 404, "not_found"},
		"нет задачи":            {"/api/v1/tasks/" + platform.IDString(newID(t)) + "/history", 404, "not_found"},
		"offset":                {path + "?offset=0", 422, "validation_error"},
		"limit 0":               {path + "?limit=0", 422, "validation_error"},
		"limit 101":             {path + "?limit=101", 422, "validation_error"},
		"курсор другой задачи":  {"/api/v1/tasks/" + other + "/history?cursor=" + url.QueryEscape(first), 422, "invalid_cursor"},
		"повреждённый курсор":   {path + "?cursor=" + url.QueryEscape("x"+first), 422, "invalid_cursor"},
		"курсор в комментариях": {"/api/v1/tasks/" + id + "/comments?cursor=" + url.QueryEscape(first), 422, "invalid_cursor"},
	} {
		t.Run(name, func(t *testing.T) {
			u := c.bob
			if name == "неучастник" {
				u = c.outsider
			}
			expect(t, c.s.Do(testutil.Request{Method: "GET", Path: req.path, Token: u.token}), req.status, req.code)
		})
	}
}

func TestCommentsHTTP(t *testing.T) {
	c := newCrew(t)
	tk := c.mustCreate(c.owner, map[string]any{"title": "Обсуждение"})
	id := tk["id"].(string)
	path := "/api/v1/tasks/" + id + "/comments"
	post := func(u user, body any) testutil.Response {
		return c.s.Do(testutil.Request{Method: "POST", Path: path, Token: u.token, Body: body})
	}

	// Комментирует member без права редактирования: автор и время — серверные, версия не меняется.
	r := post(c.bob, map[string]any{"content": "Первый"})
	expect(t, r, 201, "")
	cm := r.Map(t)
	if cm["user_id"] != c.bob.id || cm["task_id"] != id || cm["content"] != "Первый" || cm["created_at"] == nil {
		t.Fatalf("комментарий: %v", cm)
	}
	for _, text := range []string{"Второй", "Третий", "Четвёртый"} {
		expect(t, post(c.alice, map[string]any{"content": text}), 201, "")
	}
	if v := c.get(c.owner, id).Map(t)["version"]; v != float64(1) {
		t.Fatalf("комментарии изменили версию: %v", v)
	}
	if n := len(items(t, c.s.Do(testutil.Request{Method: "GET", Path: "/api/v1/tasks/" + id + "/history", Token: c.owner.token}))); n != 1 {
		t.Fatalf("комментарии попали в историю: %d", n)
	}

	// Страницы по id ASC; комментарий, добавленный между страницами, читается в конце без повторов.
	all := pages(t, c, c.owner, path, 2, func(n int) {
		if n == 1 {
			expect(t, post(c.owner, map[string]any{"content": "Пятый"}), 201, "")
		}
	})
	var texts []string
	for i, it := range all {
		texts = append(texts, it["content"].(string))
		if i > 0 && it["id"].(string) <= all[i-1]["id"].(string) {
			t.Fatalf("порядок id ASC нарушен: %v", all)
		}
	}
	if strings.Join(texts, ",") != "Первый,Второй,Третий,Четвёртый,Пятый" {
		t.Fatalf("комментарии: %v", texts)
	}

	for name, req := range map[string]struct {
		u      user
		body   any
		status int
		code   string
	}{
		"пусто":      {c.bob, map[string]any{"content": ""}, 422, "validation_error"},
		"пробелы":    {c.bob, map[string]any{"content": "  \n "}, 422, "validation_error"},
		"null":       {c.bob, map[string]any{"content": nil}, 422, "validation_error"},
		"нет поля":   {c.bob, map[string]any{}, 422, "validation_error"},
		"10001":      {c.bob, map[string]any{"content": strings.Repeat("я", 10001)}, 422, "validation_error"},
		"автор":      {c.bob, map[string]any{"content": "x", "user_id": c.alice.id}, 422, "validation_error"},
		"неучастник": {c.outsider, map[string]any{"content": "x"}, 404, "not_found"},
		"битый JSON": {c.bob, `{"content":`, 400, "invalid_json"},
	} {
		t.Run(name, func(t *testing.T) { expect(t, post(req.u, req.body), req.status, req.code) })
	}
	expect(t, post(c.bob, map[string]any{"content": strings.Repeat("я", 10000)}), 201, "")
	expect(t, c.s.Do(testutil.Request{Method: "GET", Path: path, Token: c.outsider.token}), 404, "not_found")
	expect(t, c.s.Do(testutil.Request{Method: "GET", Path: path + "?offset=2", Token: c.bob.token}), 422, "validation_error")
	expect(t, c.s.Do(testutil.Request{Method: "POST", Path: "/api/v1/tasks/" + platform.IDString(newID(t)) + "/comments",
		Token: c.bob.token, Body: map[string]any{"content": "x"}}), 404, "not_found")

	// Автор исключён (напрямую; HTTP-сценарий DELETE — в группе 9): комментарии остаются с прежним
	// авторством, а сам автор теряет доступ к обсуждению.
	if _, err := c.s.DB.Exec(`DELETE FROM team_members WHERE team_id = ? AND user_id = ?`,
		idBytes(t, c.team), idBytes(t, c.bob.id)); err != nil {
		t.Fatal(err)
	}
	all = pages(t, c, c.owner, path, 100, nil)
	if all[0]["user_id"] != c.bob.id || all[0]["content"] != "Первый" {
		t.Fatalf("комментарий исключённого автора: %v", all[0])
	}
	expect(t, c.s.Do(testutil.Request{Method: "GET", Path: path, Token: c.bob.token}), 404, "not_found")
	expect(t, post(c.bob, map[string]any{"content": "после исключения"}), 404, "not_found")
}
