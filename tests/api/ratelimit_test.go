//go:build api

package api

import (
	"strconv"
	"testing"
	"time"

	"github.com/secunda/team-tasks-service/internal/config"
	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/testutil"
)

// Превышение лимита пользователя — 429 с Retry-After до бизнес-операции; другой пользователь не
// ограничен; health-checks не считаются; токены восстанавливаются со временем.
func TestRateLimitHTTP(t *testing.T) {
	clock := &platform.FixedClock{T: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s := testutil.StartAPI(t, testutil.APIOptions{Clock: clock, Configure: func(c *config.Config) {
		c.RateUserPerMinute, c.RateUserBurst = 30, 2 // токен раз в 2 секунды
		c.RateAuthPerMinute, c.RateAuthBurst = 100000, 100000
	}})
	ann, bob := newUser(s, "ann@example.com"), newUser(s, "bob@example.com")
	create := func(u user) testutil.Response {
		return s.Do(testutil.Request{Method: "POST", Path: "/api/v1/teams", Token: u.token, Body: map[string]string{"name": "T"}})
	}
	expect(t, create(ann), 201, "")
	expect(t, create(ann), 201, "")
	r := create(ann)
	expect(t, r, 429, "rate_limit_exceeded")
	if r.Header.Get("Retry-After") != "2" {
		t.Fatalf("Retry-After: %q", r.Header.Get("Retry-After"))
	}
	var teams int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM teams`).Scan(&teams); err != nil || teams != 2 {
		t.Fatalf("запрос сверх лимита выполнил операцию: %d команд, %v", teams, err)
	}
	// Ошибочный запрос сверх лимита тоже 429: лимит проверяется раньше валидации.
	expect(t, s.Do(testutil.Request{Method: "GET", Path: "/api/v1/tasks/42", Token: ann.token}), 429, "rate_limit_exceeded")
	expect(t, create(bob), 201, "")
	for i := 0; i < 10; i++ {
		expect(t, s.Do(testutil.Request{Method: "GET", Path: "/health/live"}), 200, "")
		expect(t, s.Do(testutil.Request{Method: "GET", Path: "/health/ready"}), 200, "")
	}
	clock.Advance(2 * time.Second)
	expect(t, create(ann), 201, "")
	expect(t, create(ann), 429, "rate_limit_exceeded")
}

// Регистрация и вход делят лимит по IP; при превышении реквизиты не проверяются.
func TestAuthRateLimitByIP(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{Configure: func(c *config.Config) {
		c.RateAuthPerMinute, c.RateAuthBurst = 1, 3
	}})
	s.Register("ann@example.com", "password-for-tests", "Ann") // 1
	s.Login("ann@example.com", "password-for-tests")           // 2
	login := func(pw string) testutil.Response {
		return s.Do(testutil.Request{Method: "POST", Path: "/api/v1/login",
			Body: map[string]string{"email": "ann@example.com", "password": pw}})
	}
	expect(t, login("wrong-password"), 401, "invalid_credentials") // 3
	r := login("password-for-tests")
	expect(t, r, 429, "rate_limit_exceeded")
	if n, _ := strconv.Atoi(r.Header.Get("Retry-After")); n < 1 {
		t.Fatalf("Retry-After: %q", r.Header.Get("Retry-After"))
	}
	expect(t, s.Do(testutil.Request{Method: "POST", Path: "/api/v1/register",
		Body: map[string]string{"email": "bob@example.com", "password": "password-for-tests", "name": "Bob"}}), 429, "rate_limit_exceeded")
}

// Лимит не зависит от Redis: при недоступном Redis списки читаются из MySQL, а превышение — по-прежнему 429.
// Readiness от Redis не зависит.
func TestRateLimitWithoutRedis(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{Configure: func(c *config.Config) {
		c.RedisAddr = "127.0.0.1:1"
		c.RedisTimeout = 50 * time.Millisecond
		c.RateUserPerMinute, c.RateUserBurst = 1, 7
	}})
	expect(t, s.Do(testutil.Request{Method: "GET", Path: "/health/ready"}), 200, "")
	ann := newUser(s, "ann@example.com")
	team := createTeam(s, ann, "T") // 1
	r := s.Do(testutil.Request{Method: "POST", Path: "/api/v1/tasks", Token: ann.token,
		Body: map[string]any{"team_id": team, "title": "Задача"}}) // 2
	expect(t, r, 201, "")
	for i := 0; i < 5; i++ { // 3..7: каждый список — из MySQL, Redis не отвечает
		if got := items(t, s.Do(testutil.Request{Method: "GET", Path: "/api/v1/tasks?team_id=" + team, Token: ann.token})); len(got) != 1 {
			t.Fatalf("список без Redis: %v", got)
		}
	}
	expect(t, s.Do(testutil.Request{Method: "GET", Path: "/api/v1/tasks?team_id=" + team, Token: ann.token}), 429, "rate_limit_exceeded")
}
