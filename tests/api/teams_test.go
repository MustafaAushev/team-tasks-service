//go:build api

package api

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/testutil"
)

// user — зарегистрированный пользователь с токеном.
type user struct{ id, token string }

func newUser(s *testutil.APIServer, email string) user {
	s.T.Helper()
	id, tok := s.RegisterAndLogin(email)
	return user{id, tok}
}

func createTeam(s *testutil.APIServer, u user, name string) string {
	s.T.Helper()
	r := s.Do(testutil.Request{Method: "POST", Path: "/api/v1/teams", Token: u.token, Body: map[string]string{"name": name}})
	if r.Status != http.StatusCreated {
		s.T.Fatalf("create team: HTTP %d %s", r.Status, r.Body)
	}
	return r.Map(s.T)["id"].(string)
}

func invite(s *testutil.APIServer, u user, teamID, userID string) testutil.Response {
	s.T.Helper()
	return s.Do(testutil.Request{Method: "POST", Path: "/api/v1/teams/" + teamID + "/invite", Token: u.token,
		Body: map[string]string{"user_id": userID}})
}

func setRole(s *testutil.APIServer, u user, teamID, userID, role string) testutil.Response {
	s.T.Helper()
	return s.Do(testutil.Request{Method: "PUT", Path: "/api/v1/teams/" + teamID + "/members/" + userID + "/role",
		Token: u.token, Body: map[string]string{"role": role}})
}

func items(t *testing.T, r testutil.Response) []map[string]any {
	t.Helper()
	var body struct {
		Items []map[string]any `json:"items"`
	}
	r.JSON(t, &body)
	return body.Items
}

func expect(t *testing.T, r testutil.Response, status int, code string) {
	t.Helper()
	if r.Status != status || (code != "" && r.ErrorCode(t) != code) {
		t.Fatalf("ожидалось HTTP %d %s, получено %d: %s", status, code, r.Status, r.Body)
	}
}

func TestCreateAndListTeams(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{})
	ann, bob := newUser(s, "ann@example.com"), newUser(s, "bob@example.com")

	r := s.Do(testutil.Request{Method: "POST", Path: "/api/v1/teams", Token: ann.token, Body: map[string]string{"name": "Platform"}})
	expect(t, r, http.StatusCreated, "")
	team := r.Map(t)
	if team["role"] != "owner" || team["created_by"] != ann.id || team["name"] != "Platform" {
		t.Fatalf("команда: %v", team)
	}
	second := createTeam(s, ann, "Second")
	bobs := createTeam(s, bob, "Bob's")

	// Порядок id ASC, только свои команды.
	list := items(t, s.Do(testutil.Request{Method: "GET", Path: "/api/v1/teams", Token: ann.token}))
	if len(list) != 2 || list[0]["id"] != team["id"] || list[1]["id"] != second {
		t.Fatalf("команды Ann: %v", list)
	}
	for _, it := range list {
		if it["id"] == bobs {
			t.Fatal("чужая команда в списке")
		}
	}
	// Пустой список — массив, а не null.
	carl := newUser(s, "carl@example.com")
	if r := s.Do(testutil.Request{Method: "GET", Path: "/api/v1/teams", Token: carl.token}); !strings.Contains(string(r.Body), `"items":[]`) {
		t.Fatalf("пустой список: %s", r.Body)
	}

	// Ошибки создания.
	for name, body := range map[string]any{
		"без имени":   `{}`,
		"пустое":      map[string]string{"name": "  "},
		"121 символ":  map[string]string{"name": strings.Repeat("я", 121)},
		"лишнее поле": `{"name":"X","created_by":"` + carl.id + `"}`,
		"null":        `{"name":null}`,
	} {
		t.Run(name, func(t *testing.T) {
			expect(t, s.Do(testutil.Request{Method: "POST", Path: "/api/v1/teams", Token: carl.token, Body: body}), 422, "validation_error")
		})
	}
	expect(t, s.Do(testutil.Request{Method: "POST", Path: "/api/v1/teams", Token: carl.token, Body: `{"name":`}), 400, "invalid_json")
	expect(t, s.Do(testutil.Request{Method: "POST", Path: "/api/v1/teams", Body: map[string]string{"name": "x"}}), 401, "unauthorized")
}

// Права и изоляция команд через HTTP: 403 внутри доступной команды, 404 для чужой.
func TestTeamIsolationAndRoles(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{})
	owner, admin, member := newUser(s, "o@example.com"), newUser(s, "a@example.com"), newUser(s, "m@example.com")
	outsider, newcomer := newUser(s, "x@example.com"), newUser(s, "n@example.com")
	team := createTeam(s, owner, "Core")
	foreign := createTeam(s, outsider, "Foreign")

	// Приглашение: owner добавляет сразу как member, без принятия.
	r := invite(s, owner, team, admin.id)
	expect(t, r, http.StatusCreated, "")
	if m := r.Map(t); m["role"] != "member" || m["user_id"] != admin.id || m["team_id"] != team {
		t.Fatalf("членство: %v", m)
	}
	// Смена роли owner'ом; новые полномочия действуют сразу, без перевыдачи токена.
	r = setRole(s, owner, team, admin.id, "admin")
	expect(t, r, http.StatusOK, "")
	if r.Map(t)["role"] != "admin" {
		t.Fatalf("смена роли: %s", r.Body)
	}
	// Admin приглашает; ID в любом регистре — тот же пользователь.
	expect(t, invite(s, admin, team, strings.ToUpper(member.id)), http.StatusCreated, "")

	// Состав: участникам, порядок user_id ASC, с именами и ролями.
	members := items(t, s.Do(testutil.Request{Method: "GET", Path: "/api/v1/teams/" + team + "/members", Token: member.token}))
	if len(members) != 3 || !sort.SliceIsSorted(members, func(i, j int) bool {
		return members[i]["user_id"].(string) < members[j]["user_id"].(string)
	}) {
		t.Fatalf("состав: %v", members)
	}
	roles := map[string]string{}
	for _, m := range members {
		roles[m["user_id"].(string)] = m["role"].(string)
		if m["name"] == "" {
			t.Fatal("нет имени участника")
		}
	}
	if roles[owner.id] != "owner" || roles[admin.id] != "admin" || roles[member.id] != "member" {
		t.Fatalf("роли: %v", roles)
	}

	cases := []struct {
		name   string
		resp   func() testutil.Response
		status int
		code   string
	}{
		{"чужой состав", func() testutil.Response {
			return s.Do(testutil.Request{Method: "GET", Path: "/api/v1/teams/" + foreign + "/members", Token: member.token})
		}, 404, "not_found"},
		{"несуществующая команда", func() testutil.Response {
			return s.Do(testutil.Request{Method: "GET", Path: "/api/v1/teams/" + platform.IDString(newID(t)) + "/members", Token: member.token})
		}, 404, "not_found"},
		{"member приглашает", func() testutil.Response { return invite(s, member, team, newcomer.id) }, 403, "forbidden"},
		{"неучастник приглашает", func() testutil.Response { return invite(s, outsider, team, newcomer.id) }, 404, "not_found"},
		{"дубль приглашения", func() testutil.Response { return invite(s, owner, team, member.id) }, 409, "member_already_exists"},
		{"нет пользователя", func() testutil.Response { return invite(s, owner, team, platform.IDString(newID(t))) }, 404, "not_found"},
		{"роль в приглашении", func() testutil.Response {
			return s.Do(testutil.Request{Method: "POST", Path: "/api/v1/teams/" + team + "/invite", Token: owner.token,
				Body: map[string]string{"user_id": newcomer.id, "role": "admin"}})
		}, 422, "validation_error"},
		{"числовой user_id", func() testutil.Response {
			return s.Do(testutil.Request{Method: "POST", Path: "/api/v1/teams/" + team + "/invite", Token: owner.token, Body: `{"user_id":42}`})
		}, 422, "validation_error"},
		{"UUID v4", func() testutil.Response { return invite(s, owner, team, "6ba7b810-9dad-41d1-80b4-00c04fd430c8") }, 422, "validation_error"},
		{"admin меняет роль", func() testutil.Response { return setRole(s, admin, team, member.id, "admin") }, 403, "forbidden"},
		{"member меняет роль", func() testutil.Response { return setRole(s, member, team, member.id, "admin") }, 403, "forbidden"},
		{"выдача owner", func() testutil.Response { return setRole(s, owner, team, member.id, "owner") }, 422, "validation_error"},
		{"роль владельца", func() testutil.Response { return setRole(s, owner, team, owner.id, "member") }, 422, "owner_role_immutable"},
		{"неизвестная роль", func() testutil.Response { return setRole(s, owner, team, member.id, "boss") }, 422, "validation_error"},
		{"цель не участник", func() testutil.Response { return setRole(s, owner, team, newcomer.id, "admin") }, 404, "not_found"},
		{"смена роли в чужой команде", func() testutil.Response { return setRole(s, owner, foreign, outsider.id, "member") }, 404, "not_found"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { expect(t, c.resp(), c.status, c.code) })
	}

	// Отказы не изменили состав.
	if n := len(items(t, s.Do(testutil.Request{Method: "GET", Path: "/api/v1/teams/" + team + "/members", Token: owner.token}))); n != 3 {
		t.Fatalf("состав изменился: %d", n)
	}

	// Понижение admin: следующие запросы идут с новыми правами.
	expect(t, setRole(s, owner, team, admin.id, "member"), 200, "")
	expect(t, invite(s, admin, team, newcomer.id), 403, "forbidden")
}
