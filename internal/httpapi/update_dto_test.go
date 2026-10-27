package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

const otherV7 = "019a2469-5b00-7c33-a111-0a1b2c3d4e5f"

func TestParseUpdateTaskStates(t *testing.T) {
	t.Run("отсутствующие поля остаются нетронутыми", func(t *testing.T) {
		req, err := parseUpdateTask([]byte(`{"version": 3, "status": "done"}`))
		if err != nil {
			t.Fatal(err)
		}
		if req.Version != 3 || !req.Status.Set || req.Status.Null || req.Status.Value != "done" {
			t.Fatalf("status/version: %+v", req)
		}
		if req.Title.Set || req.Description.Set || req.AssigneeID.Set {
			t.Fatalf("отсутствующие поля помечены как переданные: %+v", req)
		}
		if got := req.Fields(); len(got) != 1 || got[0] != "status" {
			t.Fatalf("Fields=%v", got)
		}
	})

	t.Run("assignee_id null снимает назначение", func(t *testing.T) {
		req, err := parseUpdateTask([]byte(`{"version": 1, "assignee_id": null}`))
		if err != nil {
			t.Fatal(err)
		}
		if !req.AssigneeID.Set || !req.AssigneeID.Null {
			t.Fatalf("ожидался явный null: %+v", req.AssigneeID)
		}
	})

	t.Run("assignee_id значение нормализуется", func(t *testing.T) {
		req, err := parseUpdateTask([]byte(`{"version": 1, "assignee_id": "` + strings.ToUpper(otherV7) + `"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !req.AssigneeID.Set || req.AssigneeID.Null || req.AssigneeID.Value.String() != otherV7 {
			t.Fatalf("assignee: %+v", req.AssigneeID)
		}
	})

	t.Run("пустое описание — значение, а не null", func(t *testing.T) {
		req, err := parseUpdateTask([]byte(`{"version": 2, "description": ""}`))
		if err != nil {
			t.Fatal(err)
		}
		if !req.Description.Set || req.Description.Null || req.Description.Value != "" {
			t.Fatalf("description: %+v", req.Description)
		}
	})

	t.Run("все четыре поля", func(t *testing.T) {
		req, err := parseUpdateTask([]byte(`{"version": 9, "title": "T", "description": "D", "status": "todo", "assignee_id": "` + v7 + `"}`))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(req.Fields(), ","); got != "title,description,status,assignee_id" {
			t.Fatalf("Fields=%s", got)
		}
	})
}

func TestParseUpdateTaskRejects(t *testing.T) {
	cases := []struct {
		name  string
		body  string
		field string
		code  int
	}{
		{"version null", `{"version":null,"status":"done"}`, "version", 422},
		{"version строка", `{"version":"3","status":"done"}`, "version", 422},
		{"version дробная", `{"version":1.5,"status":"done"}`, "version", 422},
		{"version дробная с нулём", `{"version":1.0,"status":"done"}`, "version", 422},
		{"version экспонента", `{"version":1e2,"status":"done"}`, "version", 422},
		{"version bool", `{"version":true,"status":"done"}`, "version", 422},
		{"version объект", `{"version":{},"status":"done"}`, "version", 422},
		{"version переполнение", `{"version":99999999999999999999,"status":"done"}`, "version", 422},
		{"title число", `{"version":1,"title":5}`, "title", 422},
		{"status массив", `{"version":1,"status":["done"]}`, "status", 422},
		{"assignee_id не UUID", `{"version":1,"assignee_id":"42"}`, "assignee_id", 422},
		{"assignee_id число", `{"version":1,"assignee_id":42}`, "assignee_id", 422},
		{"assignee_id UUID v4", `{"version":1,"assignee_id":"6ba7b810-9dad-41d1-80b4-00c04fd430c8"}`, "assignee_id", 422},
		{"неизвестное поле", `{"version":1,"status":"done","priority":1}`, "priority", 422},
		{"серверное поле id", `{"version":1,"id":"` + v7 + `"}`, "id", 422},
		{"серверное поле team_id", `{"version":1,"team_id":"` + v7 + `"}`, "team_id", 422},
		{"серверное поле created_by", `{"version":1,"created_by":"` + v7 + `"}`, "created_by", 422},
		{"серверное поле closed_at", `{"version":1,"closed_at":null}`, "closed_at", 422},
		{"серверное поле updated_at", `{"version":1,"updated_at":"2026-01-01T00:00:00Z"}`, "updated_at", 422},
		{"массив вместо объекта", `[]`, "body", 422},
		{"строка вместо объекта", `"x"`, "body", 422},
		{"null вместо объекта", `null`, "body", 422},
		{"некорректный JSON", `{"version":`, "", 400},
		{"пустое тело", ``, "", 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseUpdateTask([]byte(c.body))
			if err == nil {
				t.Fatal("ожидалась ошибка")
			}
			if err.Status != c.code {
				t.Fatalf("статус %d, ожидался %d (%s)", err.Status, c.code, err.Message)
			}
			if c.field != "" {
				fields, _ := err.Details["fields"].(map[string]any)
				if _, ok := fields[c.field]; !ok {
					t.Fatalf("нет ошибки поля %q: %+v", c.field, err.Details)
				}
			}
		})
	}
}

// Правила значений (обязательная положительная version, хотя бы одно поле, null для title,
// description, status) проверяет task.UpdateInput.validate, а не транспорт: парсер лишь передаёт
// форму запроса как есть.
func TestParseUpdateTaskLeavesValueRulesToDomain(t *testing.T) {
	for name, body := range map[string]string{
		"нет version":           `{"status":"done"}`,
		"version ноль":          `{"version":0,"status":"done"}`,
		"version отрицательная": `{"version":-1,"status":"done"}`,
		"только version":        `{"version":1}`,
		"пустой объект":         `{}`,
		"title null":            `{"version":1,"title":null}`,
		"description null":      `{"version":1,"description":null}`,
		"status null":           `{"version":1,"status":null}`,
	} {
		if _, err := parseUpdateTask([]byte(body)); err != nil {
			t.Errorf("%s: транспорт отклонил %s: %s %v", name, body, err.Message, err.Details)
		}
	}
	req, _ := parseUpdateTask([]byte(`{"version":-1,"title":null}`))
	if req.Version != -1 || !req.Title.Set || !req.Title.Null {
		t.Fatalf("значения не переданы в домен: %+v", req)
	}
}

func TestParseUpdateTaskReportsAllFieldErrorsAtOnce(t *testing.T) {
	_, err := parseUpdateTask([]byte(`{"version":"0","title":5,"unknown":1}`))
	if err == nil {
		t.Fatal("ожидалась ошибка")
	}
	fields := err.Details["fields"].(map[string]any)
	for _, f := range []string{"version", "title", "unknown"} {
		if _, ok := fields[f]; !ok {
			t.Errorf("не сообщено о поле %s: %v", f, fields)
		}
	}
}

// Сквозная проверка через HTTP: DTO вызывается настоящим обработчиком маршрута.
func TestUpdateTaskDTOThroughRoute(t *testing.T) {
	op := func(w http.ResponseWriter, r *http.Request) {
		req, aerr := readUpdateTask(w, r)
		if aerr != nil {
			writeError(w, r, aerr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"version": req.Version, "changed": req.Fields(),
			"assignee_null": req.AssigneeID.Null,
		})
	}
	h := newHarness(t, Operations{UpdateTask: op})
	url := "/api/v1/tasks/" + v7

	r := h.do("PUT", url, "-", `{"version":2,"assignee_id":null}`)
	if r.Code != 200 || r.json()["assignee_null"] != true || r.json()["version"] != float64(2) {
		t.Fatalf("HTTP %d %s", r.Code, r.Body)
	}
	if r := h.do("PUT", url, "-", `{"version":"2","assignee_id":null}`); r.Code != 422 {
		t.Fatalf("строковая version: HTTP %d", r.Code)
	}
	if r := h.do("PUT", url, "-", `{"version":2`); r.Code != 400 {
		t.Fatalf("битый JSON: HTTP %d", r.Code)
	}
	big := `{"version":2,"title":"` + strings.Repeat("a", MaxBodyBytes) + `"}`
	if r := h.do("PUT", url, "-", big); r.Code != 413 {
		t.Fatalf("большое тело: HTTP %d", r.Code)
	}
}
