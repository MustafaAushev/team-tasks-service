package task

import (
	"strings"
	"unicode/utf8"

	"github.com/secunda/team-tasks-service/internal/platform"
)

const (
	maxTitleLen       = 255
	maxDescriptionLen = 10000
	maxCommentLen     = 10000
)

// Все check* дописывают ошибки в fields по имени поля и ничего не возвращают: составная проверка
// вызывает несколько check* подряд и отдаёт клиенту все ошибки разом через fieldsError.

// Непустота проверяется без внешних пробелов; сам текст не переформатируется.
func checkTitle(s string, fields map[string]string) {
	switch {
	case strings.TrimSpace(s) == "":
		fields["title"] = "обязательное непустое поле"
	case utf8.RuneCountInString(s) > maxTitleLen:
		fields["title"] = "не более 255 символов"
	}
}

func checkDescription(s string, fields map[string]string) {
	if utf8.RuneCountInString(s) > maxDescriptionLen {
		fields["description"] = "не более 10000 символов"
	}
}

func checkStatus(s string, fields map[string]string) {
	if _, ok := ParseStatus(s); !ok {
		fields["status"] = "допустимы todo, in_progress, done"
	}
}

func checkComment(s string, fields map[string]string) {
	switch {
	case strings.TrimSpace(s) == "":
		fields["content"] = "обязательное непустое поле"
	case utf8.RuneCountInString(s) > maxCommentLen:
		fields["content"] = "не более 10000 символов"
	}
}

func checkLimit(limit int, fields map[string]string) {
	if limit < 1 || limit > 100 {
		fields["limit"] = "допустимо от 1 до 100"
	}
}

// checkNonNull проверяет переданное поле частичного обновления, для которого null недопустим:
// не передано — пропуск, null — ошибка, значение — проверка check.
func checkNonNull(name string, f Field[string], check func(string, map[string]string), fields map[string]string) {
	switch {
	case !f.Set:
	case f.Null:
		fields[name] = "null недопустим"
	default:
		check(f.Value, fields)
	}
}

// checkPagination проверяет параметры страницы списка задач. mode "" — offset по умолчанию;
// offset допустим только в режиме offset, cursor — только в режиме cursor.
func checkPagination(mode string, limit int, offset *int, cursor string, fields map[string]string) {
	checkLimit(limit, fields)
	switch mode {
	case "", PageOffset:
		if offset != nil && *offset < 0 {
			fields["offset"] = "не может быть отрицательным"
		}
		if cursor != "" {
			fields["cursor"] = "курсор используется только с pagination=cursor"
		}
	case PageCursor:
		if offset != nil {
			fields["offset"] = "offset недопустим с pagination=cursor"
		}
	default:
		fields["pagination"] = "допустимы offset и cursor"
	}
}

// fieldsError превращает накопленные ошибки в ValidationError; пустая карта — nil.
func fieldsError(fields map[string]string) error {
	if len(fields) > 0 {
		return &platform.ValidationError{Fields: fields}
	}
	return nil
}
