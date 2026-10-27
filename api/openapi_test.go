package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/secunda/team-tasks-service/api"
)

func load(t *testing.T) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData(api.OpenAPISpec)
	if err != nil {
		t.Fatalf("загрузка openapi.yaml: %v", err)
	}
	return doc
}

func TestSpecIsValidOpenAPI303(t *testing.T) {
	doc := load(t)
	if doc.OpenAPI != "3.0.3" {
		t.Fatalf("версия %s, ожидалась 3.0.3", doc.OpenAPI)
	}
	// Validate проверяет и то, что все examples соответствуют своим схемам (включая UUIDv7).
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatalf("документ невалиден: %v", err)
	}
}

func TestSpecContainsAll16BusinessOperations(t *testing.T) {
	doc := load(t)
	want := map[string]string{
		"POST /api/v1/register":                         "register",
		"POST /api/v1/login":                            "login",
		"POST /api/v1/teams":                            "createTeam",
		"GET /api/v1/teams":                             "listTeams",
		"GET /api/v1/teams/{id}/members":                "listMembers",
		"POST /api/v1/teams/{id}/invite":                "inviteMember",
		"PUT /api/v1/teams/{id}/members/{user_id}/role": "changeMemberRole",
		"DELETE /api/v1/teams/{id}/members/{user_id}":   "removeMember",
		"POST /api/v1/tasks":                            "createTask",
		"GET /api/v1/tasks/{id}":                        "getTask",
		"GET /api/v1/tasks":                             "listTasks",
		"PUT /api/v1/tasks/{id}":                        "updateTask",
		"GET /api/v1/tasks/{id}/history":                "getTaskHistory",
		"POST /api/v1/tasks/{id}/comments":              "createComment",
		"GET /api/v1/tasks/{id}/comments":               "listComments",
		"GET /api/v1/teams/{team_id}/stats":             "getTeamStats",
	}
	got := map[string]string{}
	for path, item := range doc.Paths.Map() {
		if !strings.HasPrefix(path, "/api/v1/") {
			continue
		}
		for method, op := range item.Operations() {
			got[method+" "+path] = op.OperationID
		}
	}
	if len(got) != 16 {
		t.Errorf("бизнес-операций %d, ожидалось 16", len(got))
	}
	for key, id := range want {
		if got[key] != id {
			t.Errorf("%s: operationId %q, ожидался %q", key, got[key], id)
		}
	}
}

func TestSecurity(t *testing.T) {
	doc := load(t)
	public := map[string]bool{"register": true, "login": true, "healthLive": true, "healthReady": true}
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			protected := op.Security == nil || len(*op.Security) > 0
			if public[op.OperationID] && protected {
				t.Errorf("%s %s должен быть публичным (security: [])", method, path)
			}
			if !public[op.OperationID] && !protected {
				t.Errorf("%s %s должен требовать JWT", method, path)
			}
		}
	}
	if _, ok := doc.Components.SecuritySchemes["bearerAuth"]; !ok {
		t.Error("нет схемы bearerAuth")
	}
}

func isUUIDv7Ref(s *openapi3.SchemaRef) bool {
	return s != nil && (s.Ref == "#/components/schemas/UUIDv7" || s.Ref == "#/components/schemas/NullableUUIDv7")
}

// ID в path/query/JSON обязаны быть string/uuid с ограничением версии 7.
func TestIDsAreUUIDv7Strings(t *testing.T) {
	doc := load(t)

	for _, name := range []string{"UUIDv7", "NullableUUIDv7"} {
		s := doc.Components.Schemas[name].Value
		if s.Type.Slice()[0] != "string" || s.Format != "uuid" || !strings.Contains(s.Pattern, "-7[0-9a-fA-F]{3}-") {
			t.Errorf("схема %s: type=%v format=%q pattern=%q", name, s.Type, s.Format, s.Pattern)
		}
	}
	if !doc.Components.Schemas["NullableUUIDv7"].Value.Nullable {
		t.Error("NullableUUIDv7 должна быть nullable")
	}

	isIDName := func(n string) bool { return n == "id" || (strings.HasSuffix(n, "_id") && n != "request_id") }

	for name, ref := range doc.Components.Schemas {
		for prop, ps := range ref.Value.Properties {
			if isIDName(prop) && !isUUIDv7Ref(ps) {
				t.Errorf("schemas.%s.%s не ссылается на UUIDv7", name, prop)
			}
		}
	}
	check := func(where string, params openapi3.Parameters) {
		for _, p := range params {
			v := p.Value
			if v == nil {
				continue
			}
			if isIDName(v.Name) && !isUUIDv7Ref(v.Schema) {
				t.Errorf("%s: параметр %s не ссылается на UUIDv7", where, v.Name)
			}
		}
	}
	for path, item := range doc.Paths.Map() {
		check(path, item.Parameters)
		for method, op := range item.Operations() {
			check(method+" "+path, op.Parameters)
		}
	}
	for name, p := range doc.Components.Parameters {
		check("components.parameters."+name, openapi3.Parameters{&openapi3.ParameterRef{Value: p.Value}})
	}
}

// version — целое число, не UUID.
func TestVersionIsInteger(t *testing.T) {
	doc := load(t)
	for _, name := range []string{"Task", "UpdateTaskRequest"} {
		v := doc.Components.Schemas[name].Value.Properties["version"].Value
		if v.Type.Slice()[0] != "integer" {
			t.Errorf("%s.version: тип %v, ожидался integer", name, v.Type)
		}
		if v.Min == nil || *v.Min != 1 {
			t.Errorf("%s.version: ожидался minimum 1", name)
		}
	}
	req := doc.Components.Schemas["UpdateTaskRequest"].Value
	found := false
	for _, r := range req.Required {
		found = found || r == "version"
	}
	if !found {
		t.Error("UpdateTaskRequest.version должна быть обязательной")
	}
	if !doc.Components.Schemas["UpdateTaskRequest"].Value.Properties["assignee_id"].Value.Nullable &&
		doc.Components.Schemas["UpdateTaskRequest"].Value.Properties["assignee_id"].Ref == "" {
		t.Error("assignee_id должен быть nullable")
	}
}

func TestKeyExamplesPresent(t *testing.T) {
	doc := load(t)
	put := doc.Paths.Find("/api/v1/tasks/{id}").Put
	for _, name := range []string{"partial", "unassign", "clearDescription"} {
		if _, ok := put.RequestBody.Value.Content["application/json"].Examples[name]; !ok {
			t.Errorf("нет примера PUT %q", name)
		}
	}
	if _, ok := put.Responses.Status(409).Value.Content["application/json"]; !ok {
		t.Error("нет описания 409 для PUT")
	}
	del := doc.Paths.Find("/api/v1/teams/{id}/members/{user_id}").Delete
	if _, ok := del.RequestBody.Value.Content["application/json"].Examples["confirm"]; !ok {
		t.Error("нет примера DELETE с confirm_unassign")
	}
}

// Примеры нескольких страниц всех трёх курсорных списков, конфликта/повтора PUT и ошибок ролей.
func TestPaginationRoleAndConflictExamplesPresent(t *testing.T) {
	doc := load(t)
	examples := func(path, method string, status int) openapi3.Examples {
		op := doc.Paths.Find(path).GetOperation(method)
		return op.Responses.Status(status).Value.Content["application/json"].Examples
	}
	for _, c := range []struct {
		path, method string
		status       int
		names        []string
	}{
		{"/api/v1/tasks", "GET", 200, []string{"offset", "cursorFirst", "cursorLast"}},
		{"/api/v1/tasks/{id}/history", "GET", 200, []string{"firstPage", "lastPage"}},
		{"/api/v1/tasks/{id}/comments", "GET", 200, []string{"firstPage", "lastPage"}},
		{"/api/v1/teams/{id}/members/{user_id}/role", "PUT", 422, []string{"grantOwner", "ownerImmutable"}},
		{"/api/v1/tasks", "POST", 422, []string{"assigneeNotInTeam", "clientVersion"}},
	} {
		ex := examples(c.path, c.method, c.status)
		for _, name := range c.names {
			if _, ok := ex[name]; !ok {
				t.Errorf("%s %s %d: нет примера %q", c.method, c.path, c.status, name)
			}
		}
	}
	// Первая страница продолжается курсором, последняя заканчивается null.
	for _, path := range []string{"/api/v1/tasks/{id}/history", "/api/v1/tasks/{id}/comments"} {
		ex := examples(path, "GET", 200)
		first := ex["firstPage"].Value.Value.(map[string]any)["pagination"].(map[string]any)
		last := ex["lastPage"].Value.Value.(map[string]any)["pagination"].(map[string]any)
		if first["next_cursor"] == nil || last["next_cursor"] != nil {
			t.Errorf("%s: first.next_cursor=%v last.next_cursor=%v", path, first["next_cursor"], last["next_cursor"])
		}
	}
	// В журнале есть запись с несколькими полями.
	hist := examples("/api/v1/tasks/{id}/history", "GET", 200)["firstPage"].Value.Value.(map[string]any)["items"].([]any)
	if ch := hist[1].(map[string]any)["changes"].(map[string]any); len(ch) < 2 {
		t.Errorf("нет примера записи истории с несколькими полями: %v", ch)
	}
	put := doc.Paths.Find("/api/v1/tasks/{id}").Put
	if _, ok := put.RequestBody.Value.Content["application/json"].Examples["retryAfterConflict"]; !ok {
		t.Error("нет примера повтора PUT после 409")
	}
}
