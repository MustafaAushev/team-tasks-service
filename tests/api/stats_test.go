//go:build api

package api

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	apispec "github.com/secunda/team-tasks-service/api"
	"github.com/secunda/team-tasks-service/internal/config"
	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/testutil"
)

func stats(s *testutil.APIServer, u user, teamID string) testutil.Response {
	s.T.Helper()
	return s.Do(testutil.Request{Method: "GET", Path: "/api/v1/teams/" + teamID + "/stats", Token: u.token})
}

// Права и изоляция отчёта через HTTP; каждый ответ сверяется со схемой OpenAPI.
func TestTeamStatsAccessHTTP(t *testing.T) {
	c := newCrew(t)
	c.mustCreate(c.owner, map[string]any{"title": "Своя"})
	// Данные чужой команды не попадают в отчёт.
	other := createTeam(c.s, c.outsider, "Other")
	r := c.s.Do(testutil.Request{Method: "POST", Path: "/api/v1/tasks", Token: c.outsider.token,
		Body: map[string]any{"team_id": other, "title": "Чужая", "status": "done"}})
	expect(t, r, 201, "")
	expect(t, c.s.Do(testutil.Request{Method: "POST", Path: "/api/v1/tasks/" + r.Map(t)["id"].(string) + "/comments",
		Token: c.outsider.token, Body: map[string]any{"content": "чужой"}}), 201, "")

	for _, u := range []user{c.owner, c.admin} {
		r := stats(c.s, u, c.team)
		expect(t, r, http.StatusOK, "")
		body := r.Map(t)
		if body["tasks_by_status"].(map[string]any)["todo"] != float64(1) || body["tasks_by_status"].(map[string]any)["done"] != float64(0) ||
			body["comments_total"] != float64(0) {
			t.Fatalf("отчёт: %s", r.Body)
		}
	}
	for name, req := range map[string]struct {
		u      user
		team   string
		status int
		code   string
	}{
		"member":        {c.alice, c.team, 403, "forbidden"},
		"неучастник":    {c.outsider, c.team, 404, "not_found"},
		"чужая команда": {c.owner, c.foreign, 404, "not_found"},
		"нет команды":   {c.owner, platform.IDString(newID(t)), 404, "not_found"},
		"не UUIDv7":     {c.owner, "42", 422, "validation_error"},
		"без токена":    {user{}, c.team, 401, "unauthorized"},
	} {
		t.Run(name, func(t *testing.T) { expect(t, stats(c.s, req.u, req.team), req.status, req.code) })
	}

	// Пустая команда: нули, пустые массивы и null среднего — именно массивы и null, не пропуски.
	r = stats(c.s, c.outsider, c.foreign)
	expect(t, r, http.StatusOK, "")
	var empty map[string]json.RawMessage
	r.JSON(t, &empty)
	for field, want := range map[string]string{
		"tasks_by_status":         `{"todo":0,"in_progress":0,"done":0}`,
		"top_assignees":           `[]`,
		"average_close_time_days": `null`,
		"comments_total":          `0`,
	} {
		if string(empty[field]) != want {
			t.Errorf("%s = %s, ожидалось %s", field, empty[field], want)
		}
	}
}

// Пример ответа в OpenAPI воспроизводится настоящим сервером: те же числа, порядок и структура.
func TestTeamStatsMatchesOpenAPIExample(t *testing.T) {
	start := time.Date(2026, 1, 27, 12, 0, 0, 0, time.UTC)
	clock := &platform.FixedClock{T: start}
	s := testutil.StartAPI(t, testutil.APIOptions{Clock: clock, Configure: func(c *config.Config) { c.JWTTTL = 30 * 24 * time.Hour }})
	owner := newUser(s, "owner@example.com")
	annID := s.Register("ann@example.com", "password-for-tests", "Ann")
	ann := user{annID, s.Login("ann@example.com", "password-for-tests")}
	team := createTeam(s, owner, "Core")
	expect(t, invite(s, owner, team, ann.id), 201, "")

	create := func(body map[string]any) string {
		body["team_id"] = team
		r := s.Do(testutil.Request{Method: "POST", Path: "/api/v1/tasks", Token: owner.token, Body: body})
		expect(t, r, 201, "")
		return r.Map(t)["id"].(string)
	}
	put := func(id string, version int, body map[string]any) {
		body["version"] = version
		expect(t, s.Do(testutil.Request{Method: "PUT", Path: "/api/v1/tasks/" + id, Token: owner.token, Body: body}), 200, "")
	}
	comment := func(id string, n int) {
		for i := 0; i < n; i++ {
			expect(t, s.Do(testutil.Request{Method: "POST", Path: "/api/v1/tasks/" + id + "/comments", Token: owner.token,
				Body: map[string]any{"content": "c"}}), 201, "")
		}
	}
	ids := []string{
		create(map[string]any{"title": "1", "assignee_id": ann.id}),
		create(map[string]any{"title": "2", "assignee_id": ann.id}),
		create(map[string]any{"title": "3", "status": "in_progress"}),
		create(map[string]any{"title": "4"}),
	}
	comment(ids[0], 2)
	comment(ids[2], 3)
	clock.Set(start.Add(24 * time.Hour))
	put(ids[0], 1, map[string]any{"status": "done"})
	clock.Set(start.Add(3 * 24 * time.Hour))
	put(ids[1], 1, map[string]any{"status": "done"})
	clock.Set(time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC))

	r := stats(s, owner, team)
	expect(t, r, http.StatusOK, "")
	var got map[string]any
	r.JSON(t, &got)

	doc, err := openapi3.NewLoader().LoadFromData(apispec.OpenAPISpec)
	if err != nil {
		t.Fatal(err)
	}
	raw := doc.Paths.Find("/api/v1/teams/{team_id}/stats").Get.Responses.Status(200).Value.Content["application/json"].Example
	b, _ := json.Marshal(raw)
	var example map[string]any
	if err := json.Unmarshal(b, &example); err != nil {
		t.Fatal(err)
	}

	// ID в примере условные: подставляем фактические и сравниваем ответ целиком.
	example["top_assignees"].([]any)[0].(map[string]any)["user_id"] = ann.id
	want, _ := json.Marshal(example)
	have, _ := json.Marshal(got)
	if string(want) != string(have) {
		t.Fatalf("ответ не совпадает с примером OpenAPI:\nпример %s\nответ  %s", want, have)
	}
}
