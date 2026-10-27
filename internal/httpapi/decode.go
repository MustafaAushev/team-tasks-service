package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/secunda/team-tasks-service/internal/task"
)

// MaxBodyBytes — лимит тела запроса: 1 MiB.
const MaxBodyBytes = 1 << 20

// readBody читает тело с ограничением размера. Пустое тело возвращается как пустой срез.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, *apiError) {
	if r.Body == nil {
		return nil, nil
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, newError(http.StatusRequestEntityTooLarge, codeRequestTooLarge,
				"Тело запроса превышает 1 MiB")
		}
		return nil, errInvalidJSON("Не удалось прочитать тело запроса")
	}
	return b, nil
}

// decodeStrict разбирает JSON-объект в dst: неизвестные поля и типы, не подходящие полям,
// дают 422; синтаксическая ошибка, пустое тело и лишние данные после значения — 400.
func decodeStrict(w http.ResponseWriter, r *http.Request, dst any) *apiError {
	body, aerr := readBody(w, r)
	if aerr != nil {
		return aerr
	}
	return decodeBytes(body, dst)
}

func decodeBytes(body []byte, dst any) *apiError {
	if len(bytes.TrimSpace(body)) == 0 {
		return errInvalidJSON("Тело запроса пусто")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return mapDecodeError(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errInvalidJSON("После JSON-значения найдены лишние данные")
	}
	return nil
}

func mapDecodeError(err error) *apiError {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syn), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return errInvalidJSON("Некорректный JSON")
	case errors.As(err, &typ):
		field := typ.Field
		if field == "" {
			field = "body"
		}
		return errField(field, "недопустимый тип значения")
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		name := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		return errField(name, "неизвестное поле")
	default:
		return errInvalidJSON("Некорректный JSON")
	}
}

// readUpdateTask читает и разбирает тело PUT /tasks/{id} (лимит размера, 400/422 по правилам DTO).
func readUpdateTask(w http.ResponseWriter, r *http.Request) (task.UpdateInput, *apiError) {
	body, aerr := readBody(w, r)
	if aerr != nil {
		return task.UpdateInput{}, aerr
	}
	return parseUpdateTask(body)
}
