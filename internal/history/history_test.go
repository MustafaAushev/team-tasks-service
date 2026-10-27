package history

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestChangesJSON(t *testing.T) {
	raw, err := Changes{
		Title:      {Old: "a", New: "b"},
		AssigneeID: {Old: "x", New: nil},
	}.JSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"assignee_id":{"old":"x","new":null},"title":{"old":"a","new":"b"}}`
	if string(raw) != want {
		t.Errorf("JSON:\n got %s\nwant %s", raw, want)
	}
	if raw, _ := (Changes{}).JSON(); string(raw) != `{}` {
		t.Errorf("пустые изменения: %s", raw)
	}
}

func TestID(t *testing.T) {
	if ID(nil) != nil {
		t.Error("nil должен давать JSON null")
	}
	id := uuid.Must(uuid.NewV7())
	if got := ID(&id); got != strings.ToLower(id.String()) {
		t.Errorf("ID: %v", got)
	}
}

func TestTime(t *testing.T) {
	if Time(nil) != nil {
		t.Error("nil должен давать JSON null")
	}
	msk := time.FixedZone("MSK", 3*60*60)
	at := time.Date(2026, 1, 10, 12, 0, 0, 1000, msk)
	if got := Time(&at); got != "2026-01-10T09:00:00.000001Z" {
		t.Errorf("время должно быть в UTC и RFC 3339: %v", got)
	}
}
