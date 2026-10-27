//go:build e2e

// Package e2e — сквозной пользовательский сценарий против приложения, запущенного через Docker Compose
// с настоящими MySQL и Redis. Запуск: `make e2e` (поднимает отдельный стек, прогоняет тест, удаляет стек).
package e2e

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"testing"

	"github.com/secunda/team-tasks-service/internal/testutil"
)

type user struct{ id, token string }

func expect(t *testing.T, r testutil.Response, status int, code string) testutil.Response {
	t.Helper()
	if r.Status != status || (code != "" && r.ErrorCode(t) != code) {
		t.Fatalf("ожидалось HTTP %d %s, получено %d: %s", status, code, r.Status, r.Body)
	}
	if r.Header.Get("X-Request-ID") == "" {
		t.Fatalf("нет X-Request-ID: %d %s", r.Status, r.Body)
	}
	return r
}

func items(t *testing.T, r testutil.Response) []map[string]any {
	t.Helper()
	var body struct {
		Items []map[string]any `json:"items"`
	}
	r.JSON(t, &body)
	return body.Items
}

// От регистрации до отчёта и подтверждаемого исключения; каждый ответ сверяется с api/openapi.yaml.
func TestComposeScenario(t *testing.T) {
	base := os.Getenv("E2E_BASE_URL")
	if base == "" {
		t.Fatal("E2E_BASE_URL не задан: запускайте через `make e2e`")
	}
	s := testutil.Remote(t, base)
	expect(t, s.Do(testutil.Request{Method: "GET", Path: "/health/ready"}), 200, "")

	// Уникальные адреса: сценарий можно повторять на том же стеке.
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	tag := hex.EncodeToString(suffix)
	signup := func(name string) user {
		email := name + "-" + tag + "@example.com"
		id := s.Register(email, "password-e2e-1", name)
		return user{id, s.Login(email, "password-e2e-1")}
	}
	ann, bob, carl := signup("ann"), signup("bob"), signup("carl")
	do := func(u user, method, path string, body any) testutil.Response {
		t.Helper()
		return s.Do(testutil.Request{Method: method, Path: path, Token: u.token, Body: body})
	}

	// Команда, участники, роли.
	team := expect(t, do(ann, "POST", "/api/v1/teams", map[string]any{"name": "E2E " + tag}), 201, "").Map(t)["id"].(string)
	for _, u := range []user{bob, carl} {
		expect(t, do(ann, "POST", "/api/v1/teams/"+team+"/invite", map[string]any{"user_id": u.id}), 201, "")
	}
	expect(t, do(ann, "PUT", "/api/v1/teams/"+team+"/members/"+carl.id+"/role", map[string]any{"role": "admin"}), 200, "")
	if n := len(items(t, do(bob, "GET", "/api/v1/teams/"+team+"/members", nil))); n != 3 {
		t.Fatalf("состав: %d", n)
	}

	// Задачи: создание, исполнитель меняет статус, конфликт версии и повтор после чтения.
	create := func(title string, assignee *user) string {
		body := map[string]any{"team_id": team, "title": title}
		if assignee != nil {
			body["assignee_id"] = assignee.id
		}
		return expect(t, do(ann, "POST", "/api/v1/tasks", body), 201, "").Map(t)["id"].(string)
	}
	first, second := create("Подготовить релиз", &bob), create("Обновить документацию", &bob)
	create("Без исполнителя", nil)
	expect(t, do(bob, "PUT", "/api/v1/tasks/"+first, map[string]any{"version": 1, "status": "in_progress"}), 200, "")
	expect(t, do(bob, "PUT", "/api/v1/tasks/"+first, map[string]any{"version": 2, "status": "done"}), 200, "")
	expect(t, do(ann, "PUT", "/api/v1/tasks/"+first, map[string]any{"version": 1, "title": "Устаревшая правка"}), 409, "task_version_conflict")
	current := expect(t, do(ann, "GET", "/api/v1/tasks/"+first, nil), 200, "").Map(t)["version"]
	expect(t, do(ann, "PUT", "/api/v1/tasks/"+first, map[string]any{"version": current, "title": "Релиз 1.0"}), 200, "")
	expect(t, do(bob, "POST", "/api/v1/tasks/"+first+"/comments", map[string]any{"content": "Готово"}), 201, "")

	// Списки обоих режимов (второй запрос того же списка берётся из кеша) и история.
	for i := 0; i < 2; i++ {
		r := expect(t, do(bob, "GET", "/api/v1/tasks?team_id="+team+"&status=done", nil), 200, "")
		if got := items(t, r); len(got) != 1 || got[0]["id"] != first {
			t.Fatalf("done: %v", got)
		}
	}
	page := expect(t, do(bob, "GET", "/api/v1/tasks?team_id="+team+"&pagination=cursor&limit=2", nil), 200, "")
	var cur struct {
		Pagination struct {
			NextCursor *string `json:"next_cursor"`
		} `json:"pagination"`
	}
	page.JSON(t, &cur)
	if cur.Pagination.NextCursor == nil {
		t.Fatal("нет next_cursor")
	}
	if n := len(items(t, do(bob, "GET", "/api/v1/tasks/"+first+"/history", nil))); n != 4 {
		t.Fatalf("история: %d", n)
	}

	// Отчёт: owner и admin видят, member — 403.
	stats := expect(t, do(carl, "GET", "/api/v1/teams/"+team+"/stats", nil), 200, "").Map(t)
	top := stats["top_assignees"].([]any)
	if len(top) != 1 || top[0].(map[string]any)["user_id"] != bob.id || stats["comments_total"] != float64(1) {
		t.Fatalf("отчёт: %v", stats)
	}
	expect(t, do(bob, "GET", "/api/v1/teams/"+team+"/stats", nil), 403, "forbidden")

	// Исключение: предупреждение без изменений, подтверждение JSON-телом DELETE.
	warn := expect(t, do(carl, "DELETE", "/api/v1/teams/"+team+"/members/"+bob.id, nil), 409, "member_has_assigned_tasks")
	if n := warn.Map(t)["details"].(map[string]any)["assigned_tasks_count"]; n != float64(2) {
		t.Fatalf("assigned_tasks_count: %v", n)
	}
	expect(t, do(carl, "DELETE", "/api/v1/teams/"+team+"/members/"+bob.id, map[string]any{"confirm_unassign": true}), http.StatusNoContent, "")

	for _, id := range []string{first, second} {
		tk := expect(t, do(ann, "GET", "/api/v1/tasks/"+id, nil), 200, "").Map(t)
		if tk["assignee_id"] != nil {
			t.Fatalf("назначение не снято: %v", tk)
		}
		hist := items(t, do(ann, "GET", "/api/v1/tasks/"+id+"/history", nil))
		if last := hist[len(hist)-1]; last["changed_by"] != carl.id {
			t.Fatalf("автор снятия: %v", last)
		}
	}
	// Бывший участник со старым JWT: 404, в том числе на ранее закешированный список.
	for _, path := range []string{"/api/v1/tasks?team_id=" + team + "&status=done", "/api/v1/tasks/" + first + "/history"} {
		expect(t, do(bob, "GET", path, nil), 404, "not_found")
	}
	if top := expect(t, do(ann, "GET", "/api/v1/teams/"+team+"/stats", nil), 200, "").Map(t)["top_assignees"].([]any); len(top) != 0 {
		t.Fatalf("исключённый в топе: %v", top)
	}
}
