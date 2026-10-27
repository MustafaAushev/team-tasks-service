//go:build api

// Пакет api содержит HTTP-регрессию: реальные обработчики, настоящие MySQL и Redis,
// проверка каждого ответа по api/openapi.yaml.
package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/secunda/team-tasks-service/internal/httpapi"
	"github.com/secunda/team-tasks-service/internal/testutil"
)

const v7 = "019a2467-2c00-7a23-8d49-b7c0a4e52f15"

func TestInvalidJSONAndValidationErrors(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{})

	cases := []struct {
		name   string
		body   any
		status int
		code   string
	}{
		{"битый JSON", `{"email":`, 400, "invalid_json"},
		{"пустое тело", ``, 400, "invalid_json"},
		{"лишние данные", `{} {}`, 400, "invalid_json"},
		{"неизвестное поле", `{"email":"a@example.com","password":"password1","name":"A","is_admin":true}`, 422, "validation_error"},
		{"неверный тип", `{"email":5,"password":"password1","name":"A"}`, 422, "validation_error"},
		{"null для обязательного", `{"email":null,"password":"password1","name":"A"}`, 422, "validation_error"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := s.Do(testutil.Request{Method: "POST", Path: "/api/v1/register", Body: c.body})
			if r.Status != c.status || r.ErrorCode(t) != c.code {
				t.Fatalf("HTTP %d code=%s: %s", r.Status, r.ErrorCode(t), r.Body)
			}
		})
	}

	// После всех отказов в базе нет пользователей: бизнес-операция не выполнялась.
	if r := s.Do(testutil.Request{Method: "POST", Path: "/api/v1/login",
		Body: map[string]string{"email": "a@example.com", "password": "password1"}}); r.Status != 401 {
		t.Fatalf("login после невалидных регистраций: HTTP %d", r.Status)
	}
}

func TestBodyTooLarge(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{})
	huge := `{"email":"a@example.com","password":"password1","name":"` + strings.Repeat("я", httpapi.MaxBodyBytes) + `"}`
	r := s.Do(testutil.Request{Method: "POST", Path: "/api/v1/register", Body: huge})
	if r.Status != http.StatusRequestEntityTooLarge || r.ErrorCode(t) != "request_too_large" {
		t.Fatalf("HTTP %d: %.200s", r.Status, r.Body)
	}
}

func TestPathIDErrorsThroughRealHandlers(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{})
	_, token := s.RegisterAndLogin("ids@example.com")

	for _, bad := range []string{"42", "00000000-0000-0000-0000-000000000000", "6ba7b810-9dad-41d1-80b4-00c04fd430c8", "zzz"} {
		t.Run(bad, func(t *testing.T) {
			r := s.Do(testutil.Request{Method: "GET", Path: "/api/v1/tasks/" + bad, Token: token})
			if r.Status != 422 || r.ErrorCode(t) != "validation_error" {
				t.Fatalf("HTTP %d: %s", r.Status, r.Body)
			}
			if strings.Contains(strings.ToLower(string(r.Body)), "sql") {
				t.Errorf("ответ раскрывает внутренние данные: %s", r.Body)
			}
		})
	}
	// Корректный UUIDv7 любого регистра проходит валидацию (дальше — ещё не реализованная операция).
	for _, id := range []string{v7, strings.ToUpper(v7)} {
		r := s.Do(testutil.Request{Method: "GET", Path: "/api/v1/tasks/" + id, Token: token, SkipOpenAPI: true})
		if r.Status == 422 {
			t.Fatalf("валидный UUIDv7 %s отклонён: %s", id, r.Body)
		}
	}
}

func TestUnauthorizedAndUnknownRoutes(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{})

	if r := s.Do(testutil.Request{Method: "GET", Path: "/api/v1/teams"}); r.Status != 401 || r.ErrorCode(t) != "unauthorized" {
		t.Fatalf("без токена: HTTP %d %s", r.Status, r.Body)
	}
	if r := s.Do(testutil.Request{Method: "GET", Path: "/nope", SkipOpenAPI: true}); r.Status != 404 || r.ErrorCode(t) != "not_found" {
		t.Fatalf("неизвестный маршрут: HTTP %d %s", r.Status, r.Body)
	}
	if r := s.Do(testutil.Request{Method: "DELETE", Path: "/api/v1/register", SkipOpenAPI: true}); r.Status != 405 {
		t.Fatalf("неверный метод: HTTP %d", r.Status)
	}
}

func TestHealthEndpointsWithRealDependencies(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{})
	if r := s.Do(testutil.Request{Method: "GET", Path: "/health/live"}); r.Status != 200 {
		t.Fatalf("live: HTTP %d", r.Status)
	}
	r := s.Do(testutil.Request{Method: "GET", Path: "/health/ready"})
	if r.Status != 200 || r.Map(t)["schema_version"] == nil {
		t.Fatalf("ready: HTTP %d %s", r.Status, r.Body)
	}
}

func TestDocsServedByRealStack(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{})
	if r := s.Do(testutil.Request{Method: "GET", Path: "/openapi.yaml", SkipOpenAPI: true}); r.Status != 200 {
		t.Fatalf("/openapi.yaml: HTTP %d", r.Status)
	}
	if r := s.Do(testutil.Request{Method: "GET", Path: "/docs/", SkipOpenAPI: true}); r.Status != 200 {
		t.Fatalf("/docs/: HTTP %d", r.Status)
	}
}
