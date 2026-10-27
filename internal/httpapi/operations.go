package httpapi

import (
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/auth"
	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/statistics"
	"github.com/secunda/team-tasks-service/internal/task"
	"github.com/secunda/team-tasks-service/internal/team"
)

// Services — бизнес-сервисы, которые обслуживают операции API.
type Services struct {
	Logger *slog.Logger
	Auth   *auth.Service
	Teams  *team.Service
	Tasks  *task.Service
	Stats  *statistics.Service
}

// handlers — обработчики бизнес-операций поверх сервисов.
type handlers struct {
	logger *slog.Logger
	auth   *auth.Service
	teams  *team.Service
	tasks  *task.Service
	stats  *statistics.Service
}

// NewOperations связывает все маршруты /api/v1 с сервисами.
func NewOperations(s Services) Operations {
	h := &handlers{logger: s.Logger, auth: s.Auth, teams: s.Teams, tasks: s.Tasks, stats: s.Stats}
	return Operations{
		Register:      h.register,
		Login:         h.login,
		CreateTeam:    h.createTeam,
		ListTeams:     h.listTeams,
		ListMembers:   h.listMembers,
		InviteMember:  h.inviteMember,
		ChangeRole:    h.changeRole,
		RemoveMember:  h.removeMember,
		CreateTask:    h.createTask,
		GetTask:       h.getTask,
		ListTasks:     h.listTasks,
		UpdateTask:    h.updateTask,
		TaskHistory:   h.taskHistory,
		CreateComment: h.createComment,
		ListComments:  h.listComments,
		TeamStats:     h.teamStats,
	}
}

// identity — пользователь из JWT; маршрут защищённый, поэтому личность всегда есть.
func identity(r *http.Request) uuid.UUID {
	id, _ := UserID(r.Context())
	return id
}

func pathID(r *http.Request, name string) uuid.UUID {
	id, _ := PathID(r.Context(), name)
	return id
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := formatTime(*t)
	return &s
}

func idPtr(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := platform.IDString(*id)
	return &s
}

// parseBodyID разбирает обязательный UUIDv7 из тела; ошибки копятся в fields.
func parseBodyID(name string, v *string, required bool, fields map[string]string) *uuid.UUID {
	if v == nil {
		if required {
			fields[name] = "обязательное поле"
		}
		return nil
	}
	id, err := platform.ParseID(*v)
	if err != nil {
		fields[name] = "ожидается UUID версии 7"
		return nil
	}
	return &id
}

// queryValue возвращает единственное значение параметра; повтор параметра — ошибка.
func queryValue(q url.Values, name string, fields map[string]string) (string, bool) {
	vs, ok := q[name]
	if !ok {
		return "", false
	}
	if len(vs) > 1 {
		fields[name] = "параметр указан несколько раз"
		return "", false
	}
	return vs[0], true
}

// queryInt разбирает целочисленный параметр; диапазон проверяет бизнес-логика.
func queryInt(q url.Values, name string, fields map[string]string) (int, bool) {
	raw, ok := queryValue(q, name, fields)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		fields[name] = "ожидается целое число"
		return 0, false
	}
	return n, true
}

// cursorQuery разбирает limit/cursor списков, где допустима только курсорная пагинация.
func cursorQuery(r *http.Request) (limit int, cursor string, aerr *apiError) {
	q, fields := r.URL.Query(), map[string]string{}
	limit = 20
	if n, ok := queryInt(q, "limit", fields); ok {
		limit = n
	}
	cursor, _ = queryValue(q, "cursor", fields)
	if _, ok := q["offset"]; ok {
		fields["offset"] = "список поддерживает только курсорную пагинацию"
	}
	if len(fields) > 0 {
		return 0, "", errValidation(fields)
	}
	return limit, cursor, nil
}

type cursorPagination struct {
	Mode       string  `json:"mode"`
	Limit      int     `json:"limit"`
	NextCursor *string `json:"next_cursor"`
}

type offsetPagination struct {
	Mode   string `json:"mode"`
	Limit  int    `json:"limit"`
	Offset int    `json:"offset"`
	Total  int    `json:"total"`
}

type listResponse struct {
	Items      any `json:"items"`
	Pagination any `json:"pagination,omitempty"`
}

// fail отправляет доменную ошибку: известные — по контракту, остальные — безопасные 500/503.
func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	if aerr := mapDomainError(err); aerr != nil {
		writeError(w, r, aerr)
		return
	}
	writeError(w, r, unexpectedError(h.logger, r, err))
}

func mapDomainError(err error) *apiError {
	var (
		ve *platform.ValidationError
		vc *task.VersionConflictError
		at *team.AssignedTasksError
	)
	switch {
	case errors.As(err, &ve):
		return errValidation(ve.Fields)
	case errors.Is(err, auth.ErrEmailExists):
		return newError(http.StatusConflict, codeEmailExists, "Пользователь с таким email уже зарегистрирован")
	case errors.Is(err, auth.ErrInvalidCredentials):
		return newError(http.StatusUnauthorized, codeInvalidCredential, "Неверный email или пароль")
	case errors.As(err, &vc):
		return newError(http.StatusConflict, codeVersionConflict, "Версия задачи изменилась").
			with("expected_version", vc.Expected).with("current_version", vc.Current)
	case errors.As(err, &at):
		return newError(http.StatusConflict, codeHasAssignedTasks, "У участника есть назначенные задачи").
			with("assigned_tasks_count", at.Count)
	case errors.Is(err, team.ErrOwnerCannotBeRemoved):
		return newError(http.StatusUnprocessableEntity, codeOwnerNotRemovable, "Владельца команды исключить нельзя")
	case errors.Is(err, team.ErrSelfRemoval):
		return newError(http.StatusUnprocessableEntity, codeSelfRemoval, "Самостоятельный выход из команды не поддерживается")
	case errors.Is(err, team.ErrNotFound), errors.Is(err, task.ErrNotFound):
		return errNotFound()
	case errors.Is(err, team.ErrForbidden), errors.Is(err, task.ErrForbidden):
		return errForbidden()
	case errors.Is(err, team.ErrAlreadyMember):
		return newError(http.StatusConflict, codeMemberExists, "Пользователь уже состоит в команде")
	case errors.Is(err, team.ErrOwnerRoleImmutable):
		return newError(http.StatusUnprocessableEntity, codeOwnerImmutable, "Роль владельца команды изменить нельзя")
	case errors.Is(err, task.ErrAssigneeNotInTeam):
		return newError(http.StatusUnprocessableEntity, codeAssigneeNotInTeam, "Исполнитель не состоит в команде задачи")
	case errors.Is(err, task.ErrInvalidCursor):
		return newError(http.StatusUnprocessableEntity, codeInvalidCursor, "Курсор повреждён или относится к другой выборке")
	}
	return nil
}
