package platform_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
)

func TestParseID(t *testing.T) {
	v7 := "019a2467-2c00-7a23-8d49-b7c0a4e52f15"
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"валидный v7", v7, true},
		{"верхний регистр", strings.ToUpper(v7), true},
		{"число", "42", false},
		{"пусто", "", false},
		{"nil uuid", uuid.Nil.String(), false},
		{"v4", "6ba7b810-9dad-41d1-80b4-00c04fd430c8", false},
		{"v1", "6ba7b810-9dad-11d1-80b4-00c04fd430c8", false},
		{"без дефисов", strings.ReplaceAll(v7, "-", ""), false},
		{"фигурные скобки", "{" + v7 + "}", false},
		{"urn", "urn:uuid:" + v7, false},
		{"мусор той же длины", strings.Repeat("z", 36), false},
		{"вариант не RFC", "019a2467-2c00-7a23-ed49-b7c0a4e52f15", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := platform.ParseID(tt.in)
			if tt.ok {
				if err != nil {
					t.Fatalf("ожидался успех: %v", err)
				}
				if platform.IDString(got) != v7 {
					t.Fatalf("нормализация регистра: %s", got)
				}
				return
			}
			if !errors.Is(err, platform.ErrInvalidID) {
				t.Fatalf("ожидался ErrInvalidID, получено %v", err)
			}
		})
	}
}

func TestIDBytesRoundTrip(t *testing.T) {
	id, err := platform.UUIDv7Generator{}.NewID()
	if err != nil {
		t.Fatal(err)
	}
	if id.Version() != 7 {
		t.Fatalf("версия %d", id.Version())
	}
	b := platform.IDBytes(id)
	if len(b) != 16 || string(b) != string(id[:]) {
		t.Fatalf("байты не совпадают со стандартным порядком RFC")
	}
	back, err := platform.IDFromBytes(b)
	if err != nil || back != id {
		t.Fatalf("round trip: %v %v", back, err)
	}
	if _, err := platform.IDFromBytes(b[:15]); err == nil {
		t.Fatal("ожидалась ошибка для 15 байт")
	}
}
