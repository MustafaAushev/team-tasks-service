package platform

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ErrInvalidID означает, что строка не является допустимым UUIDv7 во внешней форме.
var ErrInvalidID = errors.New("invalid uuid v7")

// IDGenerator создаёт идентификаторы сущностей в Go перед INSERT.
// Ошибка генерации обязана прерывать операцию до любой записи.
type IDGenerator interface {
	NewID() (uuid.UUID, error)
}

// UUIDv7Generator генерирует UUIDv7 через google/uuid без panic.
type UUIDv7Generator struct{}

func (UUIDv7Generator) NewID() (uuid.UUID, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("generate uuid v7: %w", err)
	}
	return id, nil
}

// ParseID разбирает внешнюю форму UUIDv7: 36 символов с дефисами, любой регистр hex.
// Числа, nil UUID, другие версии и варианты, а также формы без дефисов отклоняются.
func ParseID(s string) (uuid.UUID, error) {
	if len(s) != 36 {
		return uuid.Nil, ErrInvalidID
	}
	id, err := uuid.Parse(strings.ToLower(s))
	if err != nil {
		return uuid.Nil, ErrInvalidID
	}
	if id == uuid.Nil || id.Version() != 7 || id.Variant() != uuid.RFC4122 {
		return uuid.Nil, ErrInvalidID
	}
	return id, nil
}

// IDString возвращает каноническую строку в нижнем регистре.
func IDString(id uuid.UUID) string {
	return id.String()
}

// IDBytes возвращает 16 байт в стандартном порядке RFC для BINARY(16).
// Перестановка частей UUID_TO_BIN(..., 1) намеренно не применяется.
func IDBytes(id uuid.UUID) []byte {
	b := make([]byte, 16)
	copy(b, id[:])
	return b
}

// IDFromBytes восстанавливает UUID из значения BINARY(16).
func IDFromBytes(b []byte) (uuid.UUID, error) {
	if len(b) != 16 {
		return uuid.Nil, fmt.Errorf("uuid bytes: want 16, got %d", len(b))
	}
	var id uuid.UUID
	copy(id[:], b)
	return id, nil
}
