package task

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestCursorRoundTripAndBinding(t *testing.T) {
	c := cursorCodec{secret: []byte("cursor-secret-cursor-secret-cursor-secret!")}
	sel := cursor{Kind: kindTasks, TeamID: "team-a", Status: "todo", Assignee: "user-1"}
	pos := sel
	pos.LastID = "019a2467-2c00-7a23-8d49-b7c0a4e52f15"
	token := c.encode(pos)

	got, err := c.decode(token, sel)
	if err != nil || got.LastID != pos.LastID {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if strings.ContainsAny(token, "+/=") {
		t.Errorf("курсор должен быть base64url без дополнения: %s", token)
	}

	other := cursorCodec{secret: []byte("another-secret-another-secret-another-1")}
	body, sig, _ := strings.Cut(token, ".")
	forged := base64.RawURLEncoding.EncodeToString([]byte(`{"k":"tasks","team":"team-a","status":"todo","assignee":"user-1","last_id":"x"}`))
	bad := map[string]string{
		"пусто":              "",
		"без подписи":        body,
		"мусор":              "!!!.###",
		"подпись не base64":  body + ".***",
		"чужой секрет":       other.encode(pos),
		"подменённое тело":   forged + "." + sig,
		"тело не base64":     "***." + base64.RawURLEncoding.EncodeToString(c.mac("***")),
		"тело не JSON":       "e30x." + base64.RawURLEncoding.EncodeToString(c.mac("e30x")),
		"обрезанная подпись": token[:len(token)-2],
	}
	for name, tok := range bad {
		if _, err := c.decode(tok, sel); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%s: ожидался ErrInvalidCursor, получено %v", name, err)
		}
	}

	// Курсор привязан к виду списка, команде, задаче и фильтрам.
	mismatch := map[string]cursor{
		"другая команда": {Kind: kindTasks, TeamID: "team-b", Status: "todo", Assignee: "user-1"},
		"другой статус":  {Kind: kindTasks, TeamID: "team-a", Status: "done", Assignee: "user-1"},
		"без фильтра":    {Kind: kindTasks, TeamID: "team-a", Assignee: "user-1"},
		"другой исполн.": {Kind: kindTasks, TeamID: "team-a", Status: "todo", Assignee: "user-2"},
		"история":        {Kind: kindHistory, TeamID: "team-a", Status: "todo", Assignee: "user-1"},
		"комментарии":    {Kind: kindComments, TeamID: "team-a", Status: "todo", Assignee: "user-1"},
		"другая задача":  {Kind: kindTasks, TeamID: "team-a", TaskID: "t", Status: "todo", Assignee: "user-1"},
	}
	for name, want := range mismatch {
		if _, err := c.decode(token, want); !errors.Is(err, ErrInvalidCursor) {
			t.Errorf("%s: ожидался ErrInvalidCursor, получено %v", name, err)
		}
	}
}
