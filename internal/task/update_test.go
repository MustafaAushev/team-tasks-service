package task

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/history"
)

var (
	t0 = time.Date(2026, 1, 10, 9, 0, 0, 0, time.UTC)
	t1 = time.Date(2026, 1, 11, 9, 0, 0, 0, time.UTC)
	t2 = time.Date(2026, 1, 12, 9, 0, 0, 0, time.UTC)
)

func baseTask() Task {
	return Task{ID: uuid.Must(uuid.NewV7()), Title: "T", Description: "D", Status: StatusTodo,
		CreatedBy: uuid.Must(uuid.NewV7()), Version: 3, CreatedAt: t0, UpdatedAt: t0}
}

func TestApplyClosedAtLifecycle(t *testing.T) {
	task := baseTask()

	// todo → done: закрытие ставит closed_at, журнал содержит status и closed_at.
	closed, ch := apply(task, UpdateInput{Status: Set("done")}, t1)
	if closed.ClosedAt == nil || !closed.ClosedAt.Equal(t1) {
		t.Fatalf("closed_at: %v", closed.ClosedAt)
	}
	if ch["status"] != (history.Change{Old: "todo", New: "done"}) || ch["closed_at"].Old != nil || ch["closed_at"].New != t1.Format(time.RFC3339Nano) {
		t.Fatalf("журнал закрытия: %#v", ch)
	}

	// Редактирование закрытой задачи не сдвигает дату закрытия.
	edited, ch := apply(closed, UpdateInput{Title: Set("T2"), Status: Set("done")}, t2)
	if !edited.ClosedAt.Equal(t1) || len(ch) != 1 || ch["title"] != (history.Change{Old: "T", New: "T2"}) {
		t.Fatalf("редактирование done: closed_at=%v changes=%#v", edited.ClosedAt, ch)
	}

	// done → in_progress: открытие очищает closed_at.
	reopened, ch := apply(closed, UpdateInput{Status: Set("in_progress")}, t2)
	if reopened.ClosedAt != nil || ch["closed_at"].New != nil || ch["closed_at"].Old == nil {
		t.Fatalf("открытие: closed_at=%v changes=%#v", reopened.ClosedAt, ch)
	}

	// Повторное закрытие ставит новое время.
	again, _ := apply(reopened, UpdateInput{Status: Set("done")}, t2)
	if !again.ClosedAt.Equal(t2) {
		t.Fatalf("повторное закрытие: %v", again.ClosedAt)
	}

	// todo → in_progress: closed_at в журнале не появляется.
	if _, ch := apply(task, UpdateInput{Status: Set("in_progress")}, t1); len(ch) != 1 {
		t.Fatalf("смена статуса без закрытия: %#v", ch)
	}
}

func TestApplyNoOpAndAssignee(t *testing.T) {
	task := baseTask()
	a, b := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	task.AssigneeID = &a

	// Все переданные значения совпадают — пустой diff.
	same := UpdateInput{Title: Set("T"), Description: Set("D"), Status: Set("todo"), AssigneeID: Set(a)}
	if next, ch := apply(task, same, t1); len(ch) != 0 || next.Title != "T" {
		t.Fatalf("no-op дал изменения: %#v", ch)
	}
	// Отсутствующие поля сохраняются.
	next, ch := apply(task, UpdateInput{Title: Set("New")}, t1)
	if next.Description != "D" || next.Status != StatusTodo || next.AssigneeID == nil || *next.AssigneeID != a || len(ch) != 1 {
		t.Fatalf("отсутствующие поля изменились: %+v %#v", next, ch)
	}
	// Явный null снимает исполнителя: в журнале прежний ID строкой и null.
	next, ch = apply(task, UpdateInput{AssigneeID: Null[uuid.UUID]()}, t1)
	if next.AssigneeID != nil || ch["assignee_id"] != (history.Change{Old: a.String(), New: nil}) {
		t.Fatalf("снятие исполнителя: %+v %#v", next.AssigneeID, ch)
	}
	// Переназначение и пустое описание.
	next, ch = apply(task, UpdateInput{AssigneeID: Set(b), Description: Set("")}, t1)
	if *next.AssigneeID != b || next.Description != "" || ch["assignee_id"] != (history.Change{Old: a.String(), New: b.String()}) {
		t.Fatalf("переназначение: %#v", ch)
	}
	// Назначение на задачу без исполнителя.
	task.AssigneeID = nil
	if _, ch := apply(task, UpdateInput{AssigneeID: Set(b)}, t1); ch["assignee_id"] != (history.Change{Old: nil, New: b.String()}) {
		t.Fatalf("назначение: %#v", ch)
	}
	// null для задачи без исполнителя — no-op.
	if _, ch := apply(task, UpdateInput{AssigneeID: Null[uuid.UUID]()}, t1); len(ch) != 0 {
		t.Fatalf("null к null: %#v", ch)
	}
}

// Границы значений проверяются в validate_test.go; здесь — собственные правила UpdateInput
// и то, что каждое переданное поле действительно проверяется.
func TestUpdateInputValidation(t *testing.T) {
	cases := []struct {
		name   string
		in     UpdateInput
		fields []string
	}{
		{"нет version", UpdateInput{Title: Set("x")}, []string{"version"}},
		{"отрицательная version", UpdateInput{Version: -1, Title: Set("x")}, []string{"version"}},
		{"нет полей", UpdateInput{Version: 1}, []string{"body"}},
		{"ничего не передано", UpdateInput{}, []string{"version", "body"}},
		{"title null", UpdateInput{Version: 1, Title: Null[string]()}, []string{"title"}},
		{"description null", UpdateInput{Version: 1, Description: Null[string]()}, []string{"description"}},
		{"status null", UpdateInput{Version: 1, Status: Null[string]()}, []string{"status"}},
		{"плохой title", UpdateInput{Version: 1, Title: Set(" ")}, []string{"title"}},
		{"плохой description", UpdateInput{Version: 1, Description: Set(runes(10001))}, []string{"description"}},
		{"плохой status", UpdateInput{Version: 1, Status: Set("closed")}, []string{"status"}},
		{"все ошибки разом", UpdateInput{Title: Set(""), Description: Set(runes(10001)), Status: Set("x")},
			[]string{"version", "title", "description", "status"}},
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
	for name, in := range map[string]UpdateInput{
		"все поля":        {Version: 1, Title: Set("x"), Description: Set(""), Status: Set("done"), AssigneeID: Set(uuid.Must(uuid.NewV7()))},
		"только assignee": {Version: 1, AssigneeID: Null[uuid.UUID]()},
	} {
		if err := in.validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestField(t *testing.T) {
	var absent Field[string]
	if absent.Set || absent.Null || absent.Ptr() != nil {
		t.Errorf("нулевое значение — «не передано»: %+v", absent)
	}
	if n := Null[string](); !n.Set || !n.Null || n.Ptr() != nil {
		t.Errorf("Null: %+v", n)
	}
	v := Set("x")
	if !v.Set || v.Null || v.Value != "x" || v.Ptr() == nil || *v.Ptr() != "x" {
		t.Errorf("Set: %+v", v)
	}
	if v.Ptr() == v.Ptr() {
		t.Error("Ptr должен возвращать копию, а не адрес поля")
	}
	if got := strings.Join((UpdateInput{Status: Set("done"), Title: Set("x"), AssigneeID: Null[uuid.UUID]()}).Fields(), ","); got != "title,status,assignee_id" {
		t.Errorf("Fields: %s", got)
	}
}
