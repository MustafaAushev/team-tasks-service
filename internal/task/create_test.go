package task

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/secunda/team-tasks-service/internal/history"
)

func TestCreationChanges(t *testing.T) {
	task := baseTask()
	raw, err := creationChanges(task).JSON()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]history.Change
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"title", "description", "status", "created_by", "assignee_id"} {
		if c, ok := m[f]; !ok || c.Old != nil {
			t.Errorf("поле %s: %#v", f, c)
		}
	}
	if m["created_by"].New != task.CreatedBy.String() || m["assignee_id"].New != nil {
		t.Errorf("ID в журнале должны быть строками или null: %#v", m)
	}
	if _, ok := m["closed_at"]; ok {
		t.Error("closed_at не должен появляться для незакрытой задачи")
	}
	task.ClosedAt = &t1
	if c := creationChanges(task)["closed_at"]; c.New != t1.Format(time.RFC3339Nano) {
		t.Errorf("создание в done: %#v", c)
	}
}

// Границы значений проверяются в validate_test.go; здесь — что каждое поле CreateInput проверяется.
func TestCreateInputValidation(t *testing.T) {
	cases := []struct {
		name   string
		in     CreateInput
		fields []string
	}{
		{"нет title", CreateInput{}, []string{"title"}},
		{"плохой description", CreateInput{Title: "x", Description: runes(10001)}, []string{"description"}},
		{"плохой status", CreateInput{Title: "x", Status: "closed"}, []string{"status"}},
		{"все ошибки разом", CreateInput{Description: runes(10001), Status: "x"}, []string{"title", "description", "status"}},
	}
	for _, c := range cases {
		f := fieldErr(c.in.validate())
		if len(f) != len(c.fields) {
			t.Errorf("%s: получено %v, ожидались поля %v", c.name, f, c.fields)
		}
		for _, name := range c.fields {
			if f[name] == "" {
				t.Errorf("%s: нет ошибки поля %s (%v)", c.name, name, f)
			}
		}
	}
	for name, in := range map[string]CreateInput{
		"минимум":  {Title: "x"},
		"все поля": {Title: "x", Description: "d", Status: "done"},
	} {
		if err := in.validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}
