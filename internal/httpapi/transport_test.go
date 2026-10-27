package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
)

const v7 = "019a2467-2c00-7a23-8d49-b7c0a4e52f15"

// echoID возвращает ID из пути, как его видит бизнес-обработчик (после нормализации).
func echoID(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := PathID(r.Context(), name)
		if !ok {
			writeError(w, r, errInternal())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": id.String()})
	}
}

func TestPathIDValidation(t *testing.T) {
	h := newHarness(t, Operations{GetTask: echoID("id")})
	invalid := map[string]string{
		"число":           "42",
		"слово":           "abc",
		"nil UUID":        uuid.Nil.String(),
		"UUID v4":         "6ba7b810-9dad-41d1-80b4-00c04fd430c8",
		"UUID v1":         "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		"без дефисов":     strings.ReplaceAll(v7, "-", ""),
		"фигурные скобки": "%7B" + v7 + "%7D",
		"повреждён (лишний символ)": v7 + "0",
		"повреждён (не hex)":        "019a2467-2c00-7a23-8d49-b7c0a4e52fzz",
		"пробел в конце":            v7 + "%20",
		"вариант не RFC":            "019a2467-2c00-7a23-ed49-b7c0a4e52f15",
	}
	for name, id := range invalid {
		t.Run(name, func(t *testing.T) {
			r := h.do("GET", "/api/v1/tasks/"+id, "-", nil)
			if r.Code != http.StatusUnprocessableEntity || r.code() != "validation_error" {
				t.Fatalf("HTTP %d code=%s body=%s", r.Code, r.code(), r.Body)
			}
			if _, ok := r.fields()["id"]; !ok {
				t.Fatalf("details.fields.id отсутствует: %s", r.Body)
			}
		})
	}
}

func TestPathIDNormalizesCase(t *testing.T) {
	h := newHarness(t, Operations{GetTask: echoID("id")})
	lower := h.do("GET", "/api/v1/tasks/"+v7, "-", nil)
	upper := h.do("GET", "/api/v1/tasks/"+strings.ToUpper(v7), "-", nil)
	if lower.Code != 200 || upper.Code != 200 {
		t.Fatalf("статусы %d/%d", lower.Code, upper.Code)
	}
	if lower.json()["id"] != v7 || upper.json()["id"] != v7 {
		t.Fatalf("ID разного регистра обязаны давать один канонический ID: %v / %v", lower.json(), upper.json())
	}
}

func TestEveryPathIDParameterIsValidated(t *testing.T) {
	h := newHarness(t, Operations{})
	bad := "not-a-uuid"
	cases := []struct{ method, path, param string }{
		{"GET", "/api/v1/teams/" + bad + "/members", "id"},
		{"POST", "/api/v1/teams/" + bad + "/invite", "id"},
		{"PUT", "/api/v1/teams/" + bad + "/members/" + v7 + "/role", "id"},
		{"PUT", "/api/v1/teams/" + v7 + "/members/" + bad + "/role", "user_id"},
		{"DELETE", "/api/v1/teams/" + v7 + "/members/" + bad, "user_id"},
		{"GET", "/api/v1/tasks/" + bad, "id"},
		{"PUT", "/api/v1/tasks/" + bad, "id"},
		{"GET", "/api/v1/tasks/" + bad + "/history", "id"},
		{"POST", "/api/v1/tasks/" + bad + "/comments", "id"},
		{"GET", "/api/v1/tasks/" + bad + "/comments", "id"},
		{"GET", "/api/v1/teams/" + bad + "/stats", "team_id"},
	}
	for _, c := range cases {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			r := h.do(c.method, c.path, "-", `{}`)
			if r.Code != 422 {
				t.Fatalf("HTTP %d: %s", r.Code, r.Body)
			}
			if _, ok := r.fields()[c.param]; !ok {
				t.Fatalf("ожидалась ошибка поля %s: %s", c.param, r.Body)
			}
		})
	}
}

func TestValidMissingResourceFollowsNormal404(t *testing.T) {
	notFound := func(w http.ResponseWriter, r *http.Request) { writeError(w, r, errNotFound()) }
	h := newHarness(t, Operations{GetTask: notFound})
	r := h.do("GET", "/api/v1/tasks/"+v7, "-", nil)
	if r.Code != 404 || r.code() != "not_found" {
		t.Fatalf("корректный, но отсутствующий ID должен давать 404: %d %s", r.Code, r.Body)
	}
}

func TestAuthPrecedesValidation(t *testing.T) {
	h := newHarness(t, Operations{})
	if r := h.do("GET", "/api/v1/tasks/not-a-uuid", "", nil); r.Code != 401 {
		t.Fatalf("без токена ожидался 401, получен %d", r.Code)
	}
}

func TestHandlerRunsAfterAuthAndValidation(t *testing.T) {
	h := newHarness(t, Operations{})
	if r := h.do("GET", "/api/v1/tasks/"+v7, "", nil); r.Code != 401 {
		t.Fatalf("без токена: HTTP %d %s", r.Code, r.Body)
	}
	if r := h.do("GET", "/api/v1/tasks/not-a-uuid", "-", nil); r.Code != 422 {
		t.Fatalf("плохой UUID: HTTP %d %s", r.Code, r.Body)
	}
	if r := h.do("GET", "/api/v1/tasks/"+v7, "-", nil); r.Code != 200 || r.json()["stub"] != true {
		t.Fatalf("обработчик не вызван: HTTP %d %s", r.Code, r.Body)
	}
}

// Каждый маршрут обязан иметь обработчик: пропуск — ошибка сборки приложения, а не 501 в рантайме.
func TestNewHandlerPanicsOnMissingOperation(t *testing.T) {
	ops := harnessOps(Operations{}, Operations{})
	ops.TeamStats = nil
	defer func() {
		msg, _ := recover().(string)
		if !strings.Contains(msg, "GET /api/v1/teams/{team_id}/stats") {
			t.Fatalf("ожидалась паника с названием маршрута, получено %q", msg)
		}
	}()
	NewHandler(Deps{Logger: platform.DiscardLogger(), Ops: ops})
}

func TestInvalidJSONIs400(t *testing.T) {
	h := newHarness(t, Operations{})
	bodies := map[string]string{
		"синтаксис":         `{"email": `,
		"не JSON":           `hello`,
		"пустое тело":       ``,
		"пробелы":           `   `,
		"лишние данные":     `{"email":"a@b.co","password":"password1","name":"A"} {"x":1}`,
		"два значения":      `{} []`,
		"незакрытая строка": `{"email":"a`,
	}
	for name, b := range bodies {
		t.Run(name, func(t *testing.T) {
			r := h.do("POST", "/api/v1/register", "", b)
			if r.Code != 400 || r.code() != "invalid_json" {
				t.Fatalf("HTTP %d code=%s body=%s", r.Code, r.code(), r.Body)
			}
		})
	}
}

func TestSemanticallyInvalidBodyIs422(t *testing.T) {
	h := newHarness(t, Operations{})
	cases := map[string]struct {
		body  string
		field string
	}{
		"неизвестное поле":     {`{"email":"a@b.co","password":"password1","name":"A","role":"admin"}`, "role"},
		"число вместо строки":  {`{"email":123,"password":"password1","name":"A"}`, "email"},
		"null вместо email":    {`{"email":null,"password":"password1","name":"A"}`, "email"},
		"массив":               {`[]`, "body"},
		"пропущено поле":       {`{"email":"a@b.co","password":"password1"}`, "name"},
		"объект вместо строки": {`{"email":"a@b.co","password":{"x":1},"name":"A"}`, "password"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := h.do("POST", "/api/v1/register", "", c.body)
			if r.Code != 422 || r.code() != "validation_error" {
				t.Fatalf("HTTP %d code=%s body=%s", r.Code, r.code(), r.Body)
			}
			if _, ok := r.fields()[c.field]; !ok {
				t.Fatalf("ожидалась ошибка поля %q: %s", c.field, r.Body)
			}
		})
	}
	if len(h.store.users) != 0 {
		t.Fatal("при ошибке валидации пользователь не должен создаваться")
	}
}

func TestBodyLimit(t *testing.T) {
	h := newHarness(t, Operations{})
	big := `{"email":"a@b.co","password":"password1","name":"` + strings.Repeat("a", MaxBodyBytes) + `"}`
	r := h.do("POST", "/api/v1/register", "", big)
	if r.Code != http.StatusRequestEntityTooLarge || r.code() != "request_too_large" {
		t.Fatalf("HTTP %d code=%s", r.Code, r.code())
	}
	if len(h.store.users) != 0 {
		t.Fatal("слишком большое тело не должно приводить к записи")
	}
}

func TestInternalErrorsDoNotLeakSQL(t *testing.T) {
	h := newHarness(t, Operations{})
	h.store.failErr = errBoom

	r := h.do("POST", "/api/v1/register", "", `{"email":"a@b.co","password":"password1","name":"A"}`)
	if r.Code != 500 || r.code() != "internal_error" {
		t.Fatalf("HTTP %d code=%s", r.Code, r.code())
	}
	body := r.Body.String()
	for _, leak := range []string{"SELECT", "Table", "tasks.users", "42S02", "1146", "users"} {
		if strings.Contains(body, leak) {
			t.Errorf("ответ раскрывает внутренние данные (%q): %s", leak, body)
		}
	}
	if !strings.Contains(h.logs.String(), "42S02") {
		t.Error("диагностика обязана попасть в лог")
	}

	// То же для входа.
	r = h.do("POST", "/api/v1/login", "", `{"email":"a@b.co","password":"password1"}`)
	if r.Code != 500 || strings.Contains(r.Body.String(), "SELECT") {
		t.Fatalf("login: HTTP %d %s", r.Code, r.Body)
	}
}

func TestUnknownRouteAndMethod(t *testing.T) {
	h := newHarness(t, Operations{})

	r := h.do("GET", "/api/v1/unknown", "-", nil)
	if r.Code != 404 || r.code() != "not_found" {
		t.Fatalf("неизвестный маршрут: %d %s", r.Code, r.Body)
	}

	r = h.do("DELETE", "/api/v1/tasks/"+v7+"/history", "-", nil)
	if r.Code != 405 || r.code() != "method_not_allowed" {
		t.Fatalf("метод: %d %s", r.Code, r.Body)
	}
	if allow := r.Header().Get("Allow"); allow != "GET" {
		t.Errorf("Allow=%q", allow)
	}
	if r := h.do("PATCH", "/api/v1/tasks/"+v7, "-", nil); r.Code != 405 || r.Header().Get("Allow") != "GET, PUT" {
		t.Fatalf("PATCH: %d Allow=%q", r.Code, r.Header().Get("Allow"))
	}
}

func TestErrorFormatAndRequestID(t *testing.T) {
	h := newHarness(t, Operations{})

	r := h.do("GET", "/api/v1/tasks/"+v7, "", nil)
	if r.Code != 401 {
		t.Fatalf("HTTP %d", r.Code)
	}
	m := r.json()
	for _, k := range []string{"code", "message", "request_id"} {
		if s, _ := m[k].(string); s == "" {
			t.Errorf("в ошибке нет %s: %s", k, r.Body)
		}
	}
	if m["request_id"] != r.Header().Get("X-Request-ID") {
		t.Error("request_id в теле должен совпадать с X-Request-ID")
	}
	if r.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Error("401 должен содержать WWW-Authenticate: Bearer")
	}
	if ct := r.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type=%q", ct)
	}

	// Корректный клиентский request_id принимается, недопустимый заменяется.
	req := newReq("GET", "/health/live", map[string]string{"X-Request-ID": "client-req_1.2"})
	w := serve(h, req)
	if w.Header().Get("X-Request-ID") != "client-req_1.2" {
		t.Errorf("клиентский request_id отброшен: %q", w.Header().Get("X-Request-ID"))
	}
	req = newReq("GET", "/health/live", map[string]string{"X-Request-ID": "bad id with spaces\"<>"})
	w = serve(h, req)
	if got := w.Header().Get("X-Request-ID"); got == "" || strings.ContainsAny(got, " \"<>") {
		t.Errorf("недопустимый request_id должен быть заменён: %q", got)
	}
}

func TestPanicIsRecovered(t *testing.T) {
	boom := func(http.ResponseWriter, *http.Request) { panic("kaboom secret detail") }
	h := newHarness(t, Operations{GetTask: boom})
	r := h.do("GET", "/api/v1/tasks/"+v7, "-", nil)
	if r.Code != 500 || r.code() != "internal_error" {
		t.Fatalf("HTTP %d %s", r.Code, r.Body)
	}
	if strings.Contains(r.Body.String(), "kaboom") {
		t.Error("текст panic не должен попадать в ответ")
	}
}

func TestHealthLive(t *testing.T) {
	h := newHarness(t, Operations{})
	if r := h.do("GET", "/health/live", "", nil); r.Code != 200 {
		t.Fatalf("HTTP %d", r.Code)
	}
	// Без БД readiness честно сообщает о недоступности.
	if r := h.do("GET", "/health/ready", "", nil); r.Code != 503 || r.code() != "dependency_unavailable" {
		t.Fatalf("ready без БД: %d %s", r.Code, r.Body)
	}
}

func TestDocsAndSpecAreServedLocally(t *testing.T) {
	h := newHarness(t, Operations{})

	r := h.do("GET", "/openapi.yaml", "", nil)
	if r.Code != 200 || !strings.HasPrefix(r.Header().Get("Content-Type"), "application/yaml") {
		t.Fatalf("/openapi.yaml: HTTP %d, %q", r.Code, r.Header().Get("Content-Type"))
	}
	if !strings.Contains(r.Body.String(), "openapi: 3.0.3") {
		t.Errorf("тело /openapi.yaml: %s", r.Body)
	}

	r = h.do("GET", "/docs/", "", nil)
	if r.Code != 200 || !strings.Contains(r.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("/docs/: HTTP %d", r.Code)
	}
	page := r.Body.String()
	for _, asset := range []string{"/docs/assets/swagger-ui.css", "/docs/assets/swagger-ui-bundle.js", "/docs/assets/swagger-init.js"} {
		if !strings.Contains(page, asset) {
			t.Errorf("страница не подключает %s", asset)
		}
		if ar := h.do("GET", asset, "", nil); ar.Code != 200 || ar.Body.Len() == 0 {
			t.Errorf("%s: HTTP %d, %d байт", asset, ar.Code, ar.Body.Len())
		}
	}
	if strings.Contains(page, "http://") || strings.Contains(page, "https://") {
		t.Error("страница документации не должна ссылаться на внешние ресурсы")
	}
	if init := h.do("GET", "/docs/assets/swagger-init.js", "", nil); !strings.Contains(init.Body.String(), "/openapi.yaml") {
		t.Error("инициализация должна загружать /openapi.yaml")
	}
	if r := h.do("GET", "/docs", "", nil); (r.Code != http.StatusMovedPermanently && r.Code != http.StatusTemporaryRedirect) ||
		r.Header().Get("Location") != "/docs/" {
		t.Errorf("/docs должен перенаправлять на /docs/, HTTP %d Location=%q", r.Code, r.Header().Get("Location"))
	}
	if r := h.do("GET", "/docs/assets/missing.js", "", nil); r.Code != 404 {
		t.Errorf("отсутствующий ресурс: HTTP %d", r.Code)
	}
}
