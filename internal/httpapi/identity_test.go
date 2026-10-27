package httpapi

import (
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

// protectedRoutes — все маршруты /api/v1, кроме регистрации и входа.
func protectedRoutes() []struct{ method, path string } {
	return []struct{ method, path string }{
		{"POST", "/api/v1/teams"},
		{"GET", "/api/v1/teams"},
		{"GET", "/api/v1/teams/" + v7 + "/members"},
		{"POST", "/api/v1/teams/" + v7 + "/invite"},
		{"PUT", "/api/v1/teams/" + v7 + "/members/" + otherV7 + "/role"},
		{"DELETE", "/api/v1/teams/" + v7 + "/members/" + otherV7},
		{"POST", "/api/v1/tasks"},
		{"GET", "/api/v1/tasks/" + v7},
		{"GET", "/api/v1/tasks"},
		{"PUT", "/api/v1/tasks/" + v7},
		{"GET", "/api/v1/tasks/" + v7 + "/history"},
		{"POST", "/api/v1/tasks/" + v7 + "/comments"},
		{"GET", "/api/v1/tasks/" + v7 + "/comments"},
		{"GET", "/api/v1/teams/" + v7 + "/stats"},
	}
}

func countingOps(calls *atomic.Int32) Operations {
	op := func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		id, _ := UserID(r.Context())
		writeJSON(w, http.StatusOK, map[string]string{"user_id": id.String()})
	}
	return Operations{
		CreateTeam: op, ListTeams: op, ListMembers: op, InviteMember: op, ChangeRole: op, RemoveMember: op,
		CreateTask: op, GetTask: op, ListTasks: op, UpdateTask: op, TaskHistory: op,
		CreateComment: op, ListComments: op, TeamStats: op,
	}
}

func TestAllProtectedRoutesRequireToken(t *testing.T) {
	var calls atomic.Int32
	h := newHarness(t, countingOps(&calls))
	routes := protectedRoutes()
	if len(routes) != 14 {
		t.Fatalf("ожидалось 14 защищённых маршрутов, в списке %d", len(routes))
	}
	for _, rt := range routes {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			r := h.do(rt.method, rt.path, "", `{}`)
			if r.Code != http.StatusUnauthorized || r.code() != "unauthorized" {
				t.Fatalf("без токена: HTTP %d %s", r.Code, r.Body)
			}
			if got := h.do(rt.method, rt.path, "-", `{}`); got.Code != http.StatusOK {
				t.Fatalf("с токеном: HTTP %d %s", got.Code, got.Body)
			}
		})
	}
	if want := int32(len(routes)); calls.Load() != want {
		t.Fatalf("бизнес-обработчик вызван %d раз, ожидалось %d (только с токеном)", calls.Load(), want)
	}
}

func TestPublicRoutesNeedNoToken(t *testing.T) {
	h := newHarness(t, Operations{})
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/v1/register", `{}`},
		{"POST", "/api/v1/login", `{}`},
		{"GET", "/health/live", ""},
		{"GET", "/openapi.yaml", ""},
		{"GET", "/docs/", ""},
	} {
		if r := h.do(tc.method, tc.path, "", tc.body); r.Code == http.StatusUnauthorized {
			t.Errorf("%s %s не должен требовать токен", tc.method, tc.path)
		}
	}
}

func signed(t *testing.T, method jwt.SigningMethod, key any, claims jwt.RegisteredClaims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestInvalidCredentialsAre401AndSkipBusinessLogic(t *testing.T) {
	var calls atomic.Int32
	h := newHarness(t, countingOps(&calls))
	now := h.clock.T
	secret := []byte("httpapi-test-secret-httpapi-test-secret-01")
	other := []byte("some-other-secret-some-other-secret-0123456")
	uid := h.userID.String()

	claims := func(sub string) jwt.RegisteredClaims {
		return jwt.RegisteredClaims{
			Subject: sub, Issuer: "iss", Audience: jwt.ClaimStrings{"aud"},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		}
	}
	expired := claims(uid)
	expired.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Minute))
	wrongIss := claims(uid)
	wrongIss.Issuer = "evil"
	wrongAud := claims(uid)
	wrongAud.Audience = jwt.ClaimStrings{"other"}

	headers := map[string]string{
		"нет заголовка":           "",
		"другая схема":            "Basic dXNlcjpwYXNz",
		"схема без токена":        "Bearer",
		"пустой токен":            "Bearer    ",
		"мусор":                   "Bearer not.a.jwt",
		"токен без схемы":         h.token,
		"истёк":                   "Bearer " + signed(t, jwt.SigningMethodHS256, secret, expired),
		"подпись другого секрета": "Bearer " + signed(t, jwt.SigningMethodHS256, other, claims(uid)),
		"alg none":                "Bearer " + signed(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, claims(uid)),
		"alg HS384":               "Bearer " + signed(t, jwt.SigningMethodHS384, secret, claims(uid)),
		"другой issuer":           "Bearer " + signed(t, jwt.SigningMethodHS256, secret, wrongIss),
		"другая audience":         "Bearer " + signed(t, jwt.SigningMethodHS256, secret, wrongAud),
		"sub не UUID":             "Bearer " + signed(t, jwt.SigningMethodHS256, secret, claims("42")),
		"sub пуст":                "Bearer " + signed(t, jwt.SigningMethodHS256, secret, claims("")),
		"sub UUID v4":             "Bearer " + signed(t, jwt.SigningMethodHS256, secret, claims(uuid.NewString())),
		"sub nil UUID":            "Bearer " + signed(t, jwt.SigningMethodHS256, secret, claims(uuid.Nil.String())),
	}
	for name, header := range headers {
		t.Run(name, func(t *testing.T) {
			req := newReq("GET", "/api/v1/teams", nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			w := serve(h, req)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body)
			}
			if w.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Error("нет WWW-Authenticate: Bearer")
			}
			if strings.Contains(strings.ToLower(w.Body.String()), "signature") || strings.Contains(w.Body.String(), "expired") {
				t.Errorf("ответ раскрывает причину отказа: %s", w.Body)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("бизнес-операция выполнена %d раз при недопустимом токене", calls.Load())
	}

	// Контроль: корректный токен и регистронезависимая схема проходят.
	req := newReq("GET", "/api/v1/teams", nil)
	req.Header.Set("Authorization", "bearer "+h.token)
	if w := serve(h, req); w.Code != 200 {
		t.Fatalf("валидный токен: HTTP %d", w.Code)
	}
}

func TestExpiredTokenAfterClockAdvance(t *testing.T) {
	var calls atomic.Int32
	h := newHarness(t, countingOps(&calls))
	if r := h.do("GET", "/api/v1/teams", "-", nil); r.Code != 200 {
		t.Fatalf("до истечения: HTTP %d", r.Code)
	}
	h.clock.T = h.clock.T.Add(time.Hour + time.Second)
	if r := h.do("GET", "/api/v1/teams", "-", nil); r.Code != 401 {
		t.Fatalf("после истечения: HTTP %d", r.Code)
	}
}

// Личность определяется только токеном: роли команд не передаются через JWT и middleware.
func TestIdentityIsOnlyUserID(t *testing.T) {
	var calls atomic.Int32
	h := newHarness(t, countingOps(&calls))
	for _, team := range []string{v7, otherV7} {
		r := h.do("GET", "/api/v1/teams/"+team+"/members", "-", nil)
		if r.Code != 200 || r.json()["user_id"] != h.userID.String() {
			t.Fatalf("команда %s: HTTP %d %s", team, r.Code, r.Body)
		}
	}
	// В контексте нет ничего, кроме пользователя: роль команды определяет бизнес-слой по MySQL.
	op := func(w http.ResponseWriter, r *http.Request) {
		_, hasRole := r.Context().Value(ctxKey(99)).(string)
		writeJSON(w, http.StatusOK, map[string]bool{"has_role": hasRole})
	}
	h2 := newHarness(t, Operations{ListTeams: op})
	if r := h2.do("GET", "/api/v1/teams", "-", nil); r.json()["has_role"] != false {
		t.Fatal("middleware не должен добавлять роль в контекст")
	}
}
