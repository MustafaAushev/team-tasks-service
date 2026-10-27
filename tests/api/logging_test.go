//go:build api

package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/testutil"
)

// logSink собирает JSON-строки логов приложения; запись идёт из горутин сервера.
type logSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *logSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *logSink) text() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// entry возвращает итоговую HTTP-запись запроса с данным request_id.
func (s *logSink) entry(t *testing.T, requestID string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(s.text()), "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("строка лога не JSON: %s", line)
		}
		if e["request_id"] == requestID && e["context"] == "http" {
			return e
		}
	}
	t.Fatalf("нет записи для request_id %s:\n%s", requestID, s.text())
	return nil
}

// Схема итоговой записи: верхние поля и ID, HTTP-атрибуты в meta, путь без query в message,
// ERROR для статусов от 400; секреты, тела, query и Authorization в лог не попадают.
func TestRequestLogShape(t *testing.T) {
	sink := &logSink{}
	s := testutil.StartAPI(t, testutil.APIOptions{Logger: platform.NewLogger(sink, slog.LevelInfo)})
	const password = "very-secret-password-42"
	annID := s.Register("ann@example.com", password, "Ann")
	token := s.Login("ann@example.com", password)
	ann := user{annID, token}
	team := createTeam(s, ann, "Core")
	r := s.Do(testutil.Request{Method: "POST", Path: "/api/v1/tasks", Token: token,
		Body: map[string]any{"team_id": team, "title": "Секретное тело задачи"}})
	taskID := r.Map(t)["id"].(string)

	check := func(r testutil.Response, level, message, route string, status int, code string, ids map[string]string) map[string]any {
		t.Helper()
		e := sink.entry(t, r.Header.Get("X-Request-ID"))
		for _, k := range []string{"timestamp", "level", "context", "message", "request_id", "meta"} {
			if _, ok := e[k]; !ok {
				t.Fatalf("нет верхнего поля %s: %v", k, e)
			}
		}
		meta := e["meta"].(map[string]any)
		if e["level"] != level || e["message"] != message || meta["route"] != route || meta["status"] != float64(status) ||
			meta["method"] == nil || meta["duration_ms"] == nil {
			t.Fatalf("запись: %v", e)
		}
		if got, _ := meta["error_code"].(string); got != code {
			t.Fatalf("error_code %q, ожидался %q", got, code)
		}
		for k, v := range ids {
			if e[k] != v {
				t.Fatalf("%s = %v, ожидалось %s", k, e[k], v)
			}
		}
		for _, k := range []string{"url", "query", "user_id", "task_id", "team_id", "request_id", "body"} {
			if _, ok := meta[k]; ok {
				t.Fatalf("meta содержит %s: %v", k, meta)
			}
		}
		return e
	}

	// Успех — INFO, путь без query.
	r = s.Do(testutil.Request{Method: "GET", Path: "/api/v1/tasks/" + taskID + "?limit=20&token=secret-query", Token: token})
	expect(t, r, 200, "")
	check(r, "info", "/api/v1/tasks/"+taskID, "/api/v1/tasks/{id}", 200, "", map[string]string{"user_id": annID, "task_id": taskID})

	// Конфликт версии — ERROR, в message путь и текст ошибки, task_id сверху, error_code в meta.
	r = s.Do(testutil.Request{Method: "PUT", Path: "/api/v1/tasks/" + taskID + "?trace=secret-query", Token: token,
		Body: map[string]any{"version": 7, "title": "x"}})
	expect(t, r, 409, "task_version_conflict")
	check(r, "error", "/api/v1/tasks/"+taskID+": Версия задачи изменилась", "/api/v1/tasks/{id}", 409, "task_version_conflict",
		map[string]string{"user_id": annID, "task_id": taskID})

	// Валидация query — ERROR, но значение параметра не попадает в message.
	r = s.Do(testutil.Request{Method: "GET", Path: "/api/v1/tasks?team_id=" + team + "&status=secret-status-value", Token: token})
	expect(t, r, 422, "validation_error")
	check(r, "error", "/api/v1/tasks: Данные запроса не прошли проверку", "/api/v1/tasks", 422, "validation_error",
		map[string]string{"user_id": annID})

	// 401 и 404 — тоже ERROR.
	r = s.Do(testutil.Request{Method: "GET", Path: "/api/v1/teams", Token: "garbage"})
	expect(t, r, 401, "unauthorized")
	if sink.entry(t, r.Header.Get("X-Request-ID"))["level"] != "error" {
		t.Fatal("401 не на ERROR")
	}

	// Переданный клиентом X-Request-ID сохраняется и возвращается.
	r = s.Do(testutil.Request{Method: "GET", Path: "/health/live", Header: map[string][]string{"X-Request-ID": {"client-req-1"}}})
	if r.Header.Get("X-Request-ID") != "client-req-1" || sink.entry(t, "client-req-1")["level"] != "info" {
		t.Fatal("X-Request-ID клиента не сохранён")
	}

	// В логах нет пароля, JWT, Authorization, тел запросов и значений query.
	all := sink.text()
	for _, secret := range []string{password, token, "Bearer", "Authorization", "Секретное тело задачи", "secret-query",
		"secret-status-value", "password_hash", "$2a$"} {
		if strings.Contains(all, secret) {
			t.Fatalf("в логах найдено %q", secret)
		}
	}
}
