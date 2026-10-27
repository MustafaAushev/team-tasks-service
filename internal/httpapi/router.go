// Package httpapi содержит транспортный слой: маршрутизацию, единый формат ошибок,
// разбор тел запросов, проверку личности и вспомогательные эндпоинты.
package httpapi

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/secunda/team-tasks-service/internal/auth"
	"github.com/secunda/team-tasks-service/internal/platform"
	mysqlstore "github.com/secunda/team-tasks-service/internal/storage/mysql"
)

// Operations — обработчики всех маршрутов /api/v1; все обязательны (см. NewHandler).
// Перед вызовом защищённого обработчика маршрутизатор уже проверил JWT и привёл UUIDv7 параметров
// пути к каноническому виду (см. PathID); Register и Login — публичные.
type Operations struct {
	Register      http.HandlerFunc
	Login         http.HandlerFunc
	CreateTeam    http.HandlerFunc
	ListTeams     http.HandlerFunc
	ListMembers   http.HandlerFunc
	InviteMember  http.HandlerFunc
	ChangeRole    http.HandlerFunc
	RemoveMember  http.HandlerFunc
	CreateTask    http.HandlerFunc
	GetTask       http.HandlerFunc
	ListTasks     http.HandlerFunc
	UpdateTask    http.HandlerFunc
	TaskHistory   http.HandlerFunc
	CreateComment http.HandlerFunc
	ListComments  http.HandlerFunc
	TeamStats     http.HandlerFunc
}

// Deps — зависимости HTTP-слоя.
type Deps struct {
	Logger         *slog.Logger
	Tokens         *auth.TokenManager
	DB             *sql.DB
	RequestTimeout time.Duration
	Ops            Operations
	// OpenAPI — содержимое api/openapi.yaml, отдаваемое по /openapi.yaml.
	OpenAPI []byte
	// Limits — локальные лимиты частоты; Clock — их источник времени (nil — системные часы).
	Limits RateLimits
	Clock  platform.Clock
}

// API обслуживает служебные маршруты: health-checks и документацию.
type API struct {
	logger *slog.Logger
	db     *sql.DB
	spec   []byte
}

type route struct {
	method  string
	path    string // относительно /api/v1
	ids     []pathParam
	public  bool
	handler http.HandlerFunc
}

// pathParam — UUID-параметр пути. entity говорит, чей это ID, для полей team_id/task_id в логах:
// одно и то же имя {id} в /teams/{id} и /tasks/{id} означает разные сущности.
type pathParam struct {
	name   string
	entity entity
}

type entity int

const (
	entityNone entity = iota
	entityTeam
	entityTask
)

// Имена параметров пути — как в ТЗ: /teams/{id}/invite, /teams/{team_id}/stats, /tasks/{id}.
var (
	teamParam      = pathParam{"id", entityTeam}
	statsTeamParam = pathParam{"team_id", entityTeam}
	userParam      = pathParam{"user_id", entityNone}
	taskParam      = pathParam{"id", entityTask}
)

// NewHandler строит http.Handler со всеми маршрутами `/api/v1`, health-checks, /openapi.yaml и /docs/.
// Не заданный в d.Ops обработчик — ошибка сборки приложения: NewHandler паникует при старте.
func NewHandler(d Deps) http.Handler {
	a := &API{logger: d.Logger, db: d.DB, spec: d.OpenAPI}
	mux := http.NewServeMux()

	routes := []route{
		{"POST", "/register", nil, true, d.Ops.Register},
		{"POST", "/login", nil, true, d.Ops.Login},
		{"POST", "/teams", nil, false, d.Ops.CreateTeam},
		{"GET", "/teams", nil, false, d.Ops.ListTeams},
		{"GET", "/teams/{id}/members", []pathParam{teamParam}, false, d.Ops.ListMembers},
		{"POST", "/teams/{id}/invite", []pathParam{teamParam}, false, d.Ops.InviteMember},
		{"PUT", "/teams/{id}/members/{user_id}/role", []pathParam{teamParam, userParam}, false, d.Ops.ChangeRole},
		{"DELETE", "/teams/{id}/members/{user_id}", []pathParam{teamParam, userParam}, false, d.Ops.RemoveMember},
		{"POST", "/tasks", nil, false, d.Ops.CreateTask},
		{"GET", "/tasks/{id}", []pathParam{taskParam}, false, d.Ops.GetTask},
		{"GET", "/tasks", nil, false, d.Ops.ListTasks},
		{"PUT", "/tasks/{id}", []pathParam{taskParam}, false, d.Ops.UpdateTask},
		{"GET", "/tasks/{id}/history", []pathParam{taskParam}, false, d.Ops.TaskHistory},
		{"POST", "/tasks/{id}/comments", []pathParam{taskParam}, false, d.Ops.CreateComment},
		{"GET", "/tasks/{id}/comments", []pathParam{taskParam}, false, d.Ops.ListComments},
		{"GET", "/teams/{team_id}/stats", []pathParam{statsTeamParam}, false, d.Ops.TeamStats},
	}

	// Лимиты в памяти процесса, независимо от Redis. Health-checks и документация не ограничиваются.
	userLimiter := newLimiter(d.Limits.UserPerMinute, d.Limits.UserBurst, d.Clock)
	authLimiter := newLimiter(d.Limits.AuthPerMinute, d.Limits.AuthBurst, d.Clock)

	allowed := map[string][]string{}
	for _, rt := range routes {
		if rt.handler == nil {
			panic("httpapi: не задан обработчик " + rt.method + " /api/v1" + rt.path)
		}
		h := http.Handler(rt.handler)
		// Порядок проверок: JWT (401) → лимит пользователя (429) → UUID в пути (422) → обработчик.
		// Регистрация и вход: лимит IP (429) → обработчик, реквизиты при превышении не проверяются.
		h = withPathIDs(rt.ids, h)
		if rt.public {
			h = limit(authLimiter, ipKey(d.Limits.TrustProxyHeaders), h)
		} else {
			h = authenticated(d.Tokens, limit(userLimiter, userKey, h))
		}
		full := "/api/v1" + rt.path
		mux.Handle(rt.method+" "+full, h)
		allowed[full] = append(allowed[full], rt.method)
	}
	// Тот же путь с другим методом — 405 в едином формате с заголовком Allow.
	for path, methods := range allowed {
		sort.Strings(methods)
		allow := strings.Join(methods, ", ")
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			e := newError(http.StatusMethodNotAllowed, codeMethodNotAllowed, "Метод не поддерживается для этого маршрута")
			e.Headers = map[string]string{"Allow": allow}
			writeError(w, r, e)
		})
	}

	mux.HandleFunc("GET /health/live", a.live)
	mux.HandleFunc("GET /health/ready", a.ready)
	a.mountDocs(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { writeError(w, r, errNotFound()) })

	return observe(d.Logger, d.RequestTimeout, routeLabel, mux)
}

// routeLabel возвращает шаблон маршрута без метода для группировки логов.
func routeLabel(r *http.Request) string {
	p := r.Pattern
	if _, rest, ok := strings.Cut(p, " "); ok {
		p = rest
	}
	if p == "" || p == "/" {
		return "unmatched"
	}
	return p
}

func (a *API) live(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ready проверяет MySQL и версию схемы. Redis условием готовности не является.
func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if a.db == nil {
		writeError(w, r, errUnavailable())
		return
	}
	want, err := mysqlstore.LatestVersion()
	if err != nil {
		writeError(w, r, a.unexpected(r, err))
		return
	}
	got, err := mysqlstore.Version(ctx, a.db)
	if err != nil || got != want {
		writeError(w, r, errUnavailable().with("schema_version", got).with("expected_schema_version", want))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "schema_version": got})
}

// unexpected превращает непредвиденную ошибку в безопасный ответ. Подробности только в лог.
func (a *API) unexpected(r *http.Request, err error) *apiError {
	return unexpectedError(a.logger, r, err)
}

// unexpectedError: недоступность зависимости (MySQL, исчерпанные повторы блокировок, тайм-аут или
// отмена запроса) — 503 dependency_unavailable, остальное — 500; текст ошибки клиенту не передаётся.
func unexpectedError(logger *slog.Logger, r *http.Request, err error) *apiError {
	info := infoFrom(r.Context())
	logger.LogAttrs(r.Context(), slog.LevelError, "unexpected error",
		slog.String("context", "http"),
		slog.String("request_id", info.RequestID),
		slog.Any("meta", map[string]any{"event": "unexpected_error", "error": err.Error()}))
	if mysqlstore.IsUnavailable(err) {
		return errUnavailable()
	}
	return errInternal()
}
