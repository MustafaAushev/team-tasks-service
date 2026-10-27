//go:build api

package api

import (
	"testing"

	"github.com/secunda/team-tasks-service/internal/testutil"
)

func (c *crew) remove(u user, userID string, body any) testutil.Response {
	c.s.T.Helper()
	return c.s.Do(testutil.Request{Method: "DELETE", Path: "/api/v1/teams/" + c.team + "/members/" + userID, Token: u.token, Body: body})
}

// Двухшаговое исключение через HTTP с JSON-телом DELETE: права, защита owner, предупреждение 409,
// подтверждение, побочные изменения задач и потеря доступа бывшего участника, в том числе к кешу.
func TestRemoveMemberHTTP(t *testing.T) {
	c := newCrew(t)
	open := c.mustCreate(c.owner, map[string]any{"title": "Открытая", "assignee_id": c.bob.id})
	closed := c.mustCreate(c.owner, map[string]any{"title": "Закрытая", "assignee_id": c.bob.id, "status": "done"})
	openID := open["id"].(string)
	// Бывший участник успел прочитать список — он закеширован.
	expect(t, c.s.Do(testutil.Request{Method: "GET", Path: "/api/v1/tasks?team_id=" + c.team, Token: c.bob.token}), 200, "")

	for name, req := range map[string]struct {
		u      user
		target string
		body   any
		status int
		code   string
	}{
		"member":      {c.alice, c.bob.id, nil, 403, "forbidden"},
		"неучастник":  {c.outsider, c.bob.id, nil, 404, "not_found"},
		"owner":       {c.admin, c.owner.id, nil, 422, "owner_cannot_be_removed"},
		"сам себя":    {c.admin, c.admin.id, nil, 422, "self_removal_not_supported"},
		"без тела":    {c.admin, c.bob.id, nil, 409, "member_has_assigned_tasks"},
		"false":       {c.admin, c.bob.id, map[string]any{"confirm_unassign": false}, 409, "member_has_assigned_tasks"},
		"строка":      {c.admin, c.bob.id, map[string]any{"confirm_unassign": "yes"}, 422, "validation_error"},
		"null":        {c.admin, c.bob.id, `{"confirm_unassign":null}`, 422, "validation_error"},
		"лишнее поле": {c.admin, c.bob.id, map[string]any{"confirm_unassign": true, "force": true}, 422, "validation_error"},
		"битый JSON":  {c.admin, c.bob.id, `{"confirm_unassign":`, 400, "invalid_json"},
		"не UUIDv7":   {c.admin, "42", nil, 422, "validation_error"},
		"без токена":  {user{}, c.bob.id, nil, 401, "unauthorized"},
	} {
		t.Run(name, func(t *testing.T) { expect(t, c.remove(req.u, req.target, req.body), req.status, req.code) })
	}
	// Предупреждение содержит актуальное число назначений (включая закрытую задачу) и ничего не меняет.
	r := c.remove(c.admin, c.bob.id, nil)
	if n := r.Map(t)["details"].(map[string]any)["assigned_tasks_count"]; n != float64(2) {
		t.Fatalf("assigned_tasks_count: %v", n)
	}
	if v := c.get(c.owner, openID).Map(t)["version"]; v != float64(1) {
		t.Fatalf("предупреждение изменило задачу: %v", v)
	}

	// Подтверждение: 204, назначения сняты с ростом версии и записью истории от имени admin.
	expect(t, c.remove(c.admin, c.bob.id, map[string]any{"confirm_unassign": true}), 204, "")
	for _, id := range []string{openID, closed["id"].(string)} {
		tk := c.get(c.owner, id).Map(t)
		if tk["assignee_id"] != nil || tk["version"] != float64(2) {
			t.Fatalf("задача после исключения: %v", tk)
		}
		hist := items(t, c.s.Do(testutil.Request{Method: "GET", Path: "/api/v1/tasks/" + id + "/history", Token: c.owner.token}))
		last := hist[len(hist)-1]
		ch := last["changes"].(map[string]any)["assignee_id"].(map[string]any)
		if last["changed_by"] != c.admin.id || ch["old"] != c.bob.id || ch["new"] != nil || len(last["changes"].(map[string]any)) != 1 {
			t.Fatalf("история снятия: %v", last)
		}
	}
	if tk := c.get(c.owner, closed["id"].(string)).Map(t); tk["status"] != "done" || tk["closed_at"] == nil {
		t.Fatalf("закрытая задача изменила статус: %v", tk)
	}
	// Повтор после потерянного ответа — 204 без изменений.
	expect(t, c.remove(c.admin, c.bob.id, map[string]any{"confirm_unassign": true}), 204, "")
	if v := c.get(c.owner, openID).Map(t)["version"]; v != float64(2) {
		t.Fatalf("повтор изменил задачу: %v", v)
	}

	// Бывший участник со старым JWT: 404 для списка (хотя он закеширован), задачи, истории и команды.
	for _, path := range []string{"/api/v1/tasks?team_id=" + c.team, "/api/v1/tasks/" + openID,
		"/api/v1/tasks/" + openID + "/history", "/api/v1/teams/" + c.team + "/members"} {
		expect(t, c.s.Do(testutil.Request{Method: "GET", Path: path, Token: c.bob.token}), 404, "not_found")
	}
	// Owner прочитал задачу до исключения: PUT со старой версией — 409, после нового чтения — 200.
	expect(t, c.put(c.owner, openID, map[string]any{"version": 1, "title": "Новый заголовок"}), 409, "task_version_conflict")
	expect(t, c.put(c.owner, openID, map[string]any{"version": 2, "title": "Новый заголовок"}), 200, "")

	// Без назначений подтверждение не требуется.
	expect(t, c.remove(c.owner, c.alice.id, nil), 204, "")
	members := items(t, c.s.Do(testutil.Request{Method: "GET", Path: "/api/v1/teams/" + c.team + "/members", Token: c.owner.token}))
	if len(members) != 2 {
		t.Fatalf("состав после исключений: %v", members)
	}
}
