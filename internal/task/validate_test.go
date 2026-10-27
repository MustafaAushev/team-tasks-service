package task

import (
	"errors"
	"strings"
	"testing"

	"github.com/secunda/team-tasks-service/internal/platform"
)

// fieldErr возвращает ошибки по полям из ValidationError или nil.
func fieldErr(err error) map[string]string {
	var ve *platform.ValidationError
	if !errors.As(err, &ve) {
		return nil
	}
	return ve.Fields
}

func runes(n int) string { return strings.Repeat("я", n) }

// checkField прогоняет проверку поля по таблице «значение → ожидается ли ошибка».
func checkField(t *testing.T, field string, check func(string, map[string]string), cases map[string]bool) {
	t.Helper()
	for in, wantErr := range cases {
		fields := map[string]string{}
		check(in, fields)
		if got := fields[field] != ""; got != wantErr {
			t.Errorf("%s %q: ошибка=%v, ожидалось %v (%v)", field, in[:min(len(in), 10)], got, wantErr, fields)
		}
		if len(fields) > 1 {
			t.Errorf("%s %q: лишние поля %v", field, in[:min(len(in), 10)], fields)
		}
	}
}

func TestCheckTitle(t *testing.T) {
	checkField(t, "title", checkTitle, map[string]bool{
		"":         true,
		" \n\t":    true,
		"x":        false,
		"  x  ":    false,
		runes(255): false,
		runes(256): true,
	})
}

func TestCheckDescription(t *testing.T) {
	checkField(t, "description", checkDescription, map[string]bool{
		"":           false,
		" ":          false,
		runes(10000): false,
		runes(10001): true,
	})
}

func TestCheckStatus(t *testing.T) {
	checkField(t, "status", checkStatus, map[string]bool{
		"todo":        false,
		"in_progress": false,
		"done":        false,
		"":            true,
		"closed":      true,
		"DONE":        true,
	})
}

func TestCheckComment(t *testing.T) {
	checkField(t, "content", checkComment, map[string]bool{
		"":           true,
		" \n\t":      true,
		"x":          false,
		runes(10000): false,
		runes(10001): true,
	})
}

func TestCheckLimit(t *testing.T) {
	for limit, wantErr := range map[int]bool{-1: true, 0: true, 1: false, 20: false, 100: false, 101: true} {
		fields := map[string]string{}
		checkLimit(limit, fields)
		if got := fields["limit"] != ""; got != wantErr || len(fields) > 1 {
			t.Errorf("limit %d: %v, ожидалась ошибка=%v", limit, fields, wantErr)
		}
	}
}

func TestCheckPagination(t *testing.T) {
	neg, zero := -1, 0
	cases := []struct {
		name   string
		mode   string
		limit  int
		offset *int
		cursor string
		fields []string
	}{
		{"offset по умолчанию", "", 20, nil, "", nil},
		{"offset явно", PageOffset, 20, &zero, "", nil},
		{"cursor без токена", PageCursor, 20, nil, "", nil},
		{"cursor с токеном", PageCursor, 20, nil, "tok", nil},
		{"плохой limit", "", 0, nil, "", []string{"limit"}},
		{"отрицательный offset", PageOffset, 20, &neg, "", []string{"offset"}},
		{"cursor в режиме offset", "", 20, nil, "tok", []string{"cursor"}},
		{"offset в режиме cursor", PageCursor, 20, &zero, "", []string{"offset"}},
		{"неизвестный режим", "page", 20, nil, "", []string{"pagination"}},
		{"все ошибки разом", PageOffset, 101, &neg, "tok", []string{"limit", "offset", "cursor"}},
	}
	for _, c := range cases {
		fields := map[string]string{}
		checkPagination(c.mode, c.limit, c.offset, c.cursor, fields)
		if len(fields) != len(c.fields) {
			t.Errorf("%s: получено %v, ожидались поля %v", c.name, fields, c.fields)
		}
		for _, name := range c.fields {
			if fields[name] == "" {
				t.Errorf("%s: нет ошибки поля %s (%v)", c.name, name, fields)
			}
		}
	}
}

func TestFieldsError(t *testing.T) {
	if err := fieldsError(map[string]string{}); err != nil {
		t.Errorf("пустая карта: %v", err)
	}
	f := fieldErr(fieldsError(map[string]string{"title": "x", "status": "y"}))
	if len(f) != 2 || f["title"] != "x" || f["status"] != "y" {
		t.Errorf("ошибки по полям: %v", f)
	}
}
