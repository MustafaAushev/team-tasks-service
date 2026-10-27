package httpapi

import (
	"errors"
	"net/http"
)

// Коды ошибок API. Они входят в контракт и описаны в api/openapi.yaml.
const (
	codeInvalidJSON       = "invalid_json"
	codeValidation        = "validation_error"
	codeUnauthorized      = "unauthorized"
	codeNotFound          = "not_found"
	codeMethodNotAllowed  = "method_not_allowed"
	codeRequestTooLarge   = "request_too_large"
	codeInternal          = "internal_error"
	codeUnavailable       = "dependency_unavailable"
	codeEmailExists       = "email_already_exists"
	codeInvalidCredential = "invalid_credentials"
	codeForbidden         = "forbidden"
	codeMemberExists      = "member_already_exists"
	codeOwnerImmutable    = "owner_role_immutable"
	codeAssigneeNotInTeam = "assignee_not_in_team"
	codeVersionConflict   = "task_version_conflict"
	codeInvalidCursor     = "invalid_cursor"
	codeHasAssignedTasks  = "member_has_assigned_tasks"
	codeOwnerNotRemovable = "owner_cannot_be_removed"
	codeSelfRemoval       = "self_removal_not_supported"
)

// apiError — ошибка с HTTP-статусом и безопасным для клиента текстом.
// Внутренние причины (SQL и др.) в неё не попадают.
type apiError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
	Headers map[string]string
}

func (e *apiError) Error() string { return e.Message }

func newError(status int, code, msg string) *apiError {
	return &apiError{Status: status, Code: code, Message: msg}
}

func (e *apiError) with(key string, val any) *apiError {
	if e.Details == nil {
		e.Details = map[string]any{}
	}
	e.Details[key] = val
	return e
}

func errInvalidJSON(msg string) *apiError {
	return newError(http.StatusBadRequest, codeInvalidJSON, msg)
}

// errValidation описывает ошибки полей: details.fields — объект «поле → причина».
func errValidation(fields map[string]string) *apiError {
	e := newError(http.StatusUnprocessableEntity, codeValidation, "Данные запроса не прошли проверку")
	m := make(map[string]any, len(fields))
	for k, v := range fields {
		m[k] = v
	}
	return e.with("fields", m)
}

func errField(field, reason string) *apiError {
	return errValidation(map[string]string{field: reason})
}

func errUnauthorized() *apiError {
	e := newError(http.StatusUnauthorized, codeUnauthorized, "Требуется действительный токен доступа")
	e.Headers = map[string]string{"WWW-Authenticate": "Bearer"}
	return e
}

func errNotFound() *apiError {
	return newError(http.StatusNotFound, codeNotFound, "Ресурс не найден")
}

func errForbidden() *apiError {
	return newError(http.StatusForbidden, codeForbidden, "Недостаточно прав для операции")
}

func errInternal() *apiError {
	return newError(http.StatusInternalServerError, codeInternal, "Внутренняя ошибка сервера")
}

func errUnavailable() *apiError {
	return newError(http.StatusServiceUnavailable, codeUnavailable, "Зависимость временно недоступна, повторите запрос позже")
}

func asAPIError(err error) *apiError {
	var ae *apiError
	if errors.As(err, &ae) {
		return ae
	}
	return nil
}
