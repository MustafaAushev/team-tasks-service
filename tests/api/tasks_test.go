//go:build api

package api

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/secunda/team-tasks-service/internal/testutil"
)

// crew — команда с owner, admin, двумя member и посторонним пользователем со своей командой.
type crew struct {
	s                                  *testutil.APIServer
	team, foreign                      string
	owner, admin, alice, bob, outsider user
}

func newCrew(t *testing.T) *crew {
	t.Helper()
	s := testutil.StartAPI(t, testutil.APIOptions{})
	c := &crew{s: s,
		owner: newUser(s, "owner@example.com"), admin: newUser(s, "admin@example.com"),
		alice: newUser(s, "alice@example.com"), bob: newUser(s, "bob@example.com"), outsider: newUser(s, "out@example.com")}
	c.team = createTeam(s, c.owner, "Core")
	for _, u := range []user{c.admin, c.alice, c.bob} {
		expect(t, invite(s, c.owner, c.team, u.id), 201, "")
	}
	expect(t, setRole(s, c.owner, c.team, c.admin.id, "admin"), 200, "")
	c.foreign = createTeam(s, c.outsider, "Foreign")
	return c
}

func (c *crew) create(u user, body any) testutil.Response {
	c.s.T.Helper()
	return c.s.Do(testutil.Request{Method: "POST", Path: "/api/v1/tasks", Token: u.token, Body: body})
}

// mustCreate создаёт задачу в команде crew (если team_id не задан) и возвращает тело ответа.
func (c *crew) mustCreate(u user, body map[string]any) map[string]any {
	c.s.T.Helper()
	if _, ok := body["team_id"]; !ok {
		body["team_id"] = c.team
	}
	r := c.create(u, body)
	expect(c.s.T, r, http.StatusCreated, "")
	return r.Map(c.s.T)
}

func (c *crew) get(u user, id string) testutil.Response {
	c.s.T.Helper()
	return c.s.Do(testutil.Request{Method: "GET", Path: "/api/v1/tasks/" + id, Token: u.token})
}

func (c *crew) put(u user, id string, body any) testutil.Response {
	c.s.T.Helper()
	return c.s.Do(testutil.Request{Method: "PUT", Path: "/api/v1/tasks/" + id, Token: u.token, Body: body})
}

func (c *crew) list(u user, query string) testutil.Response {
	c.s.T.Helper()
	return c.s.Do(testutil.Request{Method: "GET", Path: "/api/v1/tasks?" + query, Token: u.token})
}

func pagination(t *testing.T, r testutil.Response) map[string]any {
	t.Helper()
	p, _ := r.Map(t)["pagination"].(map[string]any)
	return p
}

func TestCreateAndGetTask(t *testing.T) {
	c := newCrew(t)

	// Минимальное тело: автор из токена, значения по умолчанию, version 1.
	r := c.create(c.alice, map[string]any{"team_id": c.team, "title": "Минимальная"})
	expect(t, r, 201, "")
	tk := r.Map(t)
	if tk["created_by"] != c.alice.id || tk["status"] != "todo" || tk["description"] != "" ||
		tk["assignee_id"] != nil || tk["version"] != float64(1) || tk["closed_at"] != nil || tk["team_id"] != c.team {
		t.Fatalf("задача: %v", tk)
	}
	// Создание сразу в done и с исполнителем из команды.
	done := c.mustCreate(c.owner, map[string]any{"title": "Готово", "status": "done", "assignee_id": c.bob.id, "description": "d"})
	if done["closed_at"] == nil || done["assignee_id"] != c.bob.id {
		t.Fatalf("done: %v", done)
	}

	// Чтение участником и изоляция.
	got := c.get(c.bob, tk["id"].(string))
	expect(t, got, 200, "")
	if got.Map(t)["version"] != float64(1) {
		t.Fatalf("GET: %s", got.Body)
	}
	foreignRead := c.get(c.outsider, tk["id"].(string))
	expect(t, foreignRead, 404, "not_found")
	if strings.Contains(string(foreignRead.Body), "Минимальная") || strings.Contains(string(foreignRead.Body), c.team) {
		t.Fatal("404 раскрывает содержимое задачи или команду")
	}
	expect(t, c.get(c.alice, newID(t).String()), 404, "not_found")

	cases := map[string]struct {
		body   any
		status int
		code   string
	}{
		"исполнитель вне команды": {map[string]any{"team_id": c.team, "title": "x", "assignee_id": c.outsider.id}, 422, "assignee_not_in_team"},
		"чужая команда":           {map[string]any{"team_id": c.foreign, "title": "x"}, 404, "not_found"},
		"без team_id":             {map[string]any{"title": "x"}, 422, "validation_error"},
		"без title":               {map[string]any{"team_id": c.team}, 422, "validation_error"},
		"null title":              {map[string]any{"team_id": c.team, "title": nil}, 422, "validation_error"},
		"пробелы в title":         {map[string]any{"team_id": c.team, "title": "   "}, 422, "validation_error"},
		"title 256":               {map[string]any{"team_id": c.team, "title": strings.Repeat("я", 256)}, 422, "validation_error"},
		"описание 10001":          {map[string]any{"team_id": c.team, "title": "x", "description": strings.Repeat("я", 10001)}, 422, "validation_error"},
		"неизвестный статус":      {map[string]any{"team_id": c.team, "title": "x", "status": "closed"}, 422, "validation_error"},
		"пустой статус":           {map[string]any{"team_id": c.team, "title": "x", "status": ""}, 422, "validation_error"},
		"клиентская version":      {map[string]any{"team_id": c.team, "title": "x", "version": 5}, 422, "validation_error"},
		"created_by":              {map[string]any{"team_id": c.team, "title": "x", "created_by": c.bob.id}, 422, "validation_error"},
		"числовой team_id":        {map[string]any{"team_id": 1, "title": "x"}, 422, "validation_error"},
		"nil UUID":                {map[string]any{"team_id": "00000000-0000-0000-0000-000000000000", "title": "x"}, 422, "validation_error"},
		"битый JSON":              {`{"team_id":`, 400, "invalid_json"},
	}
	for name, cs := range cases {
		t.Run(name, func(t *testing.T) { expect(t, c.create(c.alice, cs.body), cs.status, cs.code) })
	}
	// Отказы не создали задач.
	if p := pagination(t, c.list(c.owner, "team_id="+c.team)); p["total"] != float64(2) {
		t.Fatalf("total после отказов: %v", p)
	}
}

func TestUpdateTaskPermissionsAndNull(t *testing.T) {
	c := newCrew(t)
	tk := c.mustCreate(c.alice, map[string]any{"title": "Alice", "description": "D", "assignee_id": c.bob.id})
	id := tk["id"].(string)

	// Исполнитель-member меняет только status; title, даже совпадающий, — 403 целиком.
	expect(t, c.put(c.bob, id, map[string]any{"version": 1, "status": "in_progress", "title": "Alice"}), 403, "forbidden")
	r := c.put(c.bob, id, map[string]any{"version": 1, "status": "in_progress"})
	expect(t, r, 200, "")
	// Посторонний member — 403; неучастник — 404.
	expect(t, c.put(c.outsider, id, map[string]any{"version": 2, "status": "done"}), 404, "not_found")
	other := c.mustCreate(c.owner, map[string]any{"title": "Owner's"})
	expect(t, c.put(c.alice, other["id"].(string), map[string]any{"version": 1, "status": "done"}), 403, "forbidden")

	// Автор меняет все поля своей задачи; отсутствующие поля сохраняются.
	r = c.put(c.alice, id, map[string]any{"version": 2, "title": "Alice 2"})
	expect(t, r, 200, "")
	if m := r.Map(t); m["description"] != "D" || m["status"] != "in_progress" || m["assignee_id"] != c.bob.id || m["version"] != float64(3) {
		t.Fatalf("незаданные поля изменились: %v", m)
	}
	// Явный null снимает исполнителя; пустая строка очищает описание.
	r = c.put(c.alice, id, map[string]any{"version": 3, "assignee_id": nil, "description": ""})
	expect(t, r, 200, "")
	if m := r.Map(t); m["assignee_id"] != nil || m["description"] != "" || m["version"] != float64(4) {
		t.Fatalf("null и пустое описание: %v", m)
	}
	// Admin меняет чужую задачу, назначая исполнителя в любом регистре.
	r = c.put(c.admin, id, map[string]any{"version": 4, "assignee_id": strings.ToUpper(c.alice.id)})
	expect(t, r, 200, "")
	if r.Map(t)["assignee_id"] != c.alice.id {
		t.Fatalf("регистр ID: %s", r.Body)
	}

	// Ошибки тела: 422 до любой записи.
	for name, body := range map[string]any{
		"нет version":        map[string]any{"title": "x"},
		"null version":       map[string]any{"version": nil, "title": "x"},
		"строковая version":  map[string]any{"version": "5", "title": "x"},
		"дробная version":    `{"version": 5.5, "title": "x"}`,
		"нулевая version":    map[string]any{"version": 0, "title": "x"},
		"null title":         map[string]any{"version": 5, "title": nil, "status": "done"},
		"null status":        map[string]any{"version": 5, "status": nil},
		"null description":   map[string]any{"version": 5, "description": nil},
		"только version":     map[string]any{"version": 5},
		"серверное поле":     map[string]any{"version": 5, "closed_at": nil},
		"неизвестный статус": map[string]any{"version": 5, "status": "closed"},
		"исполнитель вне":    map[string]any{"version": 5, "assignee_id": c.outsider.id},
		"title 256":          map[string]any{"version": 5, "title": strings.Repeat("я", 256)},
	} {
		t.Run(name, func(t *testing.T) {
			r := c.put(c.owner, id, body)
			if r.Status != 422 {
				t.Fatalf("HTTP %d: %s", r.Status, r.Body)
			}
		})
	}
	if v := c.get(c.owner, id).Map(t)["version"]; v != float64(5) {
		t.Fatalf("отказы изменили версию: %v", v)
	}
	expect(t, c.put(c.owner, newID(t).String(), map[string]any{"version": 1, "title": "x"}), 404, "not_found")
}

// Пример README: GET → PUT → 409 → новое чтение → осознанный повтор; no-op и closed_at.
func TestOptimisticUpdateFlow(t *testing.T) {
	c := newCrew(t)
	id := c.mustCreate(c.owner, map[string]any{"title": "Релиз"})["id"].(string)

	// Два клиента прочитали version 1.
	v := c.get(c.owner, id).Map(t)["version"]
	if v != float64(1) {
		t.Fatalf("version: %v", v)
	}
	expect(t, c.put(c.admin, id, map[string]any{"version": 1, "status": "done"}), 200, "")
	r := c.put(c.owner, id, map[string]any{"version": 1, "title": "Релиз 1.0"})
	expect(t, r, 409, "task_version_conflict")
	details, _ := r.Map(t)["details"].(map[string]any)
	if details["expected_version"] != float64(1) || details["current_version"] != float64(2) {
		t.Fatalf("details: %v", details)
	}
	// Совпадающие значения со старой версией — тоже 409, а не no-op.
	expect(t, c.put(c.owner, id, map[string]any{"version": 1, "status": "done"}), 409, "task_version_conflict")

	// Новое чтение и повтор правки с актуальной версией; закрытая задача сохраняет closed_at.
	fresh := c.get(c.owner, id).Map(t)
	r = c.put(c.owner, id, map[string]any{"version": fresh["version"], "title": "Релиз 1.0"})
	expect(t, r, 200, "")
	updated := r.Map(t)
	if updated["version"] != float64(3) || updated["closed_at"] != fresh["closed_at"] || updated["status"] != "done" {
		t.Fatalf("повтор: %v", updated)
	}
	// No-op с актуальной версией: 200 без новой версии и updated_at.
	r = c.put(c.owner, id, map[string]any{"version": 3, "title": "Релиз 1.0", "status": "done"})
	expect(t, r, 200, "")
	if m := r.Map(t); m["version"] != float64(3) || m["updated_at"] != updated["updated_at"] {
		t.Fatalf("no-op: %v", m)
	}
	// Открытие очищает closed_at.
	r = c.put(c.owner, id, map[string]any{"version": 3, "status": "todo"})
	expect(t, r, 200, "")
	if r.Map(t)["closed_at"] != nil {
		t.Fatalf("открытие: %s", r.Body)
	}
	// В журнале только успешные ревизии 1..4.
	hist := items(t, c.s.Do(testutil.Request{Method: "GET", Path: "/api/v1/tasks/" + id + "/history", Token: c.bob.token}))
	if len(hist) != 4 {
		t.Fatalf("записей истории %d, ожидалось 4", len(hist))
	}
}

func TestListTasksOffsetAndFilters(t *testing.T) {
	c := newCrew(t)
	var ids []string
	for i := range 5 {
		body := map[string]any{"title": "T"}
		if i%2 == 0 {
			body["assignee_id"] = c.bob.id
		}
		ids = append(ids, c.mustCreate(c.owner, body)["id"].(string))
	}
	expect(t, c.put(c.owner, ids[0], map[string]any{"version": 1, "status": "done"}), 200, "")
	c.mustCreate(c.outsider, map[string]any{"team_id": c.foreign, "title": "Чужая"})

	// Параметры примера задания: team_id, status, assignee_id, limit, offset.
	q := url.Values{"team_id": {c.team}, "status": {"todo"}, "assignee_id": {c.bob.id}, "limit": {"20"}, "offset": {"0"}}
	r := c.list(c.alice, q.Encode())
	expect(t, r, 200, "")
	got := items(t, r)
	if p := pagination(t, r); p["mode"] != "offset" || p["total"] != float64(2) || p["limit"] != float64(20) || p["offset"] != float64(0) {
		t.Fatalf("pagination: %v", p)
	}
	if len(got) != 2 || got[0]["id"] != ids[4] || got[1]["id"] != ids[2] {
		t.Fatalf("фильтры И и порядок id DESC: %v", got)
	}

	// Значения по умолчанию и границы limit/offset.
	r = c.list(c.alice, "team_id="+c.team)
	if p := pagination(t, r); p["limit"] != float64(20) || p["offset"] != float64(0) || p["total"] != float64(5) || len(items(t, r)) != 5 {
		t.Fatalf("по умолчанию: %v", p)
	}
	r = c.list(c.alice, "team_id="+c.team+"&limit=2&offset=4")
	if got := items(t, r); len(got) != 1 || got[0]["id"] != ids[0] || pagination(t, r)["total"] != float64(5) {
		t.Fatalf("последняя страница: %v", got)
	}
	if got := items(t, c.list(c.alice, "team_id="+c.team+"&offset=50")); len(got) != 0 {
		t.Fatalf("offset за концом: %v", got)
	}
	if got := items(t, c.list(c.alice, "team_id="+strings.ToUpper(c.team)+"&limit=100")); len(got) != 5 {
		t.Fatalf("team_id в верхнем регистре: %d", len(got))
	}

	for name, query := range map[string]string{
		"нет team_id":      "status=todo",
		"limit 0":          "team_id=" + c.team + "&limit=0",
		"limit 101":        "team_id=" + c.team + "&limit=101",
		"limit не число":   "team_id=" + c.team + "&limit=abc",
		"offset < 0":       "team_id=" + c.team + "&offset=-1",
		"статус":           "team_id=" + c.team + "&status=closed",
		"assignee v4":      "team_id=" + c.team + "&assignee_id=6ba7b810-9dad-41d1-80b4-00c04fd430c8",
		"числовой team_id": "team_id=1",
		"режим":            "team_id=" + c.team + "&pagination=pages",
		"два team_id":      "team_id=" + c.team + "&team_id=" + c.foreign,
		"cursor в offset":  "team_id=" + c.team + "&cursor=abc",
		"offset в cursor":  "team_id=" + c.team + "&pagination=cursor&offset=0",
	} {
		t.Run(name, func(t *testing.T) { expect(t, c.list(c.alice, query), 422, "validation_error") })
	}
	expect(t, c.list(c.alice, "team_id="+c.foreign), 404, "not_found")
	expect(t, c.list(c.alice, "team_id="+newID(t).String()), 404, "not_found")
}

func TestListTasksCursor(t *testing.T) {
	c := newCrew(t)
	var ids []string
	for range 5 {
		ids = append(ids, c.mustCreate(c.owner, map[string]any{"title": "T"})["id"].(string))
	}

	base := "team_id=" + c.team + "&pagination=cursor&limit=2"
	r := c.list(c.alice, base)
	expect(t, r, 200, "")
	p := pagination(t, r)
	if p["mode"] != "cursor" || p["next_cursor"] == nil {
		t.Fatalf("первая страница: %v", p)
	}
	if _, ok := p["total"]; ok {
		t.Fatal("в режиме cursor total не возвращается")
	}
	var seen []string
	for _, it := range items(t, r) {
		seen = append(seen, it["id"].(string))
	}
	first := p["next_cursor"].(string)

	// Новая задача с бо́льшим ID перед прочитанной страницей не вызывает повторов.
	c.mustCreate(c.owner, map[string]any{"title": "Новая"})
	cursor := first
	for {
		r = c.list(c.alice, base+"&cursor="+url.QueryEscape(cursor))
		expect(t, r, 200, "")
		for _, it := range items(t, r) {
			seen = append(seen, it["id"].(string))
		}
		next := pagination(t, r)["next_cursor"]
		if next == nil {
			break
		}
		cursor = next.(string)
	}
	if strings.Join(seen, ",") != strings.Join([]string{ids[4], ids[3], ids[2], ids[1], ids[0]}, ",") {
		t.Fatalf("страницы: %v", seen)
	}

	// Курсор привязан к выборке, повреждение и offset — 422. Курсор проверяется до чтения данных,
	// поэтому чужой курсор не раскрывает ничего ни участнику другой команды, ни неучастнику.
	for name, req := range map[string]struct {
		u     user
		query string
	}{
		"другой фильтр": {c.alice, base + "&status=todo&cursor=" + url.QueryEscape(first)},
		"повреждён":     {c.alice, base + "&cursor=" + url.QueryEscape(first[:len(first)-1]+"A")},
		"мусор":         {c.alice, base + "&cursor=garbage"},
		"участник другой команды":  {c.outsider, "team_id=" + c.foreign + "&pagination=cursor&cursor=" + url.QueryEscape(first)},
		"неучастник с чужим курс.": {c.alice, "team_id=" + c.foreign + "&pagination=cursor&cursor=" + url.QueryEscape(first)},
	} {
		t.Run(name, func(t *testing.T) { expect(t, c.list(req.u, req.query), 422, "invalid_cursor") })
	}
	expect(t, c.list(c.alice, "team_id="+c.foreign+"&pagination=cursor"), 404, "not_found")
	expect(t, c.list(c.alice, base+"&offset=2"), 422, "validation_error")
}
