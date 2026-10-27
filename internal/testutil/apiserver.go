package testutil

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
	"github.com/redis/go-redis/v9"

	"github.com/secunda/team-tasks-service/api"
	"github.com/secunda/team-tasks-service/internal/auth"
	"github.com/secunda/team-tasks-service/internal/cache"
	"github.com/secunda/team-tasks-service/internal/config"
	"github.com/secunda/team-tasks-service/internal/httpapi"
	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/statistics"
	mysqlstore "github.com/secunda/team-tasks-service/internal/storage/mysql"
	"github.com/secunda/team-tasks-service/internal/task"
	"github.com/secunda/team-tasks-service/internal/team"
)

// APIOptions настраивает тестовый сервер. Нулевое значение — настоящие обработчики всех операций.
type APIOptions struct {
	// Ops заменяет отдельные обработчики; незаданные остаются настоящими.
	Ops   httpapi.Operations
	Clock platform.Clock
	IDs   platform.IDGenerator
	// Configure позволяет переопределить конфигурацию (например, лимиты или адрес Redis) до запуска.
	Configure func(*config.Config)
	// Logger получает логи приложения (по умолчанию отбрасываются).
	Logger *slog.Logger
}

// APIServer — настоящий HTTP-стек на изолированной базе MySQL и настоящем Redis.
type APIServer struct {
	T      *testing.T
	URL    string
	DB     *sql.DB
	Config config.Config
	Clock  platform.Clock
	Tokens *auth.TokenManager
	Auth   *auth.Service
	Teams  *team.Service
	Tasks  *task.Service
	// Cache — кеш списков задач на настоящем Redis; ключи команд теста удаляются после теста.
	Cache *cache.ListCache
	Redis *redis.Client

	router routers.Router
	client *http.Client
}

// StartAPI поднимает httptest-сервер с реальными обработчиками.
func StartAPI(t *testing.T, opts APIOptions) *APIServer {
	t.Helper()
	db, cfg := MySQL(t)
	_ = Redis(t) // убеждаемся, что Redis доступен: набор запрошен целиком
	if opts.Configure != nil {
		opts.Configure(&cfg)
	}
	var clock platform.Clock = platform.SystemClock{}
	if opts.Clock != nil {
		clock = opts.Clock
	}
	var ids platform.IDGenerator = platform.UUIDv7Generator{}
	if opts.IDs != nil {
		ids = opts.IDs
	}

	logger := opts.Logger
	if logger == nil {
		logger = platform.DiscardLogger()
	}
	return serve(t, db, cfg, clock, ids, logger, opts.Ops)
}

// Fork поднимает второй, независимый экземпляр приложения (новые сервисы и HTTP-сервер) над той же
// базой и конфигурацией. Нужен для проверок, что сервер не хранит состояние между запросами, например
// что курсор, выданный одним экземпляром, продолжается другим.
func (s *APIServer) Fork() *APIServer {
	s.T.Helper()
	return serve(s.T, s.DB, s.Config, s.Clock, platform.UUIDv7Generator{}, platform.DiscardLogger(), httpapi.Operations{})
}

// mergeOperations возвращает base, в которой заданные в override обработчики заменяют исходные.
// Поля перебираются через reflect, поэтому новая операция в httpapi.Operations учитывается сама.
func mergeOperations(base, override httpapi.Operations) httpapi.Operations {
	b, o := reflect.ValueOf(&base).Elem(), reflect.ValueOf(override)
	for i := range b.NumField() {
		if f := o.Field(i); !f.IsNil() {
			b.Field(i).Set(f)
		}
	}
	return base
}

func serve(t *testing.T, db *sql.DB, cfg config.Config, clock platform.Clock, ids platform.IDGenerator, logger *slog.Logger, override httpapi.Operations) *APIServer {
	t.Helper()
	tokens := auth.NewTokenManager(cfg.JWTSecret, cfg.JWTIssuer, cfg.JWTAudience, cfg.JWTTTL, clock)
	svc, err := auth.NewService(mysqlstore.UserStore{DB: db}, tokens, ids, clock, cfg.BcryptCost)
	if err != nil {
		t.Fatalf("auth service: %v", err)
	}
	teams := team.NewService(mysqlstore.TeamStore{DB: db}, ids, clock)
	// Кеш работает с Redis из конфигурации (тест может задать недоступный адрес); очистка ключей —
	// через общий тестовый Redis.
	rc := Redis(t)
	appRedis := cache.NewClient(cfg)
	t.Cleanup(func() { _ = appRedis.Close() })
	listCache := cache.NewListCache(appRedis, cfg, clock, logger)
	t.Cleanup(func() { DeleteTeamCacheKeys(t, db, rc) })
	tasks := task.NewService(mysqlstore.TaskStore{DB: db}, ids, clock, cfg.CursorSecret).WithListCache(listCache)
	stats := statistics.NewService(mysqlstore.StatsStore{DB: db}, clock)
	ops := httpapi.NewOperations(httpapi.Services{Logger: logger, Auth: svc, Teams: teams, Tasks: tasks, Stats: stats})
	h := httpapi.NewHandler(httpapi.Deps{
		Logger:         logger,
		Tokens:         tokens,
		DB:             db,
		RequestTimeout: cfg.RequestTimeout,
		Ops:            mergeOperations(ops, override),
		OpenAPI:        api.OpenAPISpec,
		Limits: httpapi.RateLimits{
			UserPerMinute: cfg.RateUserPerMinute, UserBurst: cfg.RateUserBurst,
			AuthPerMinute: cfg.RateAuthPerMinute, AuthBurst: cfg.RateAuthBurst,
			TrustProxyHeaders: cfg.TrustProxyHeaders,
		},
		Clock: clock,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	return &APIServer{T: t, URL: srv.URL, DB: db, Config: cfg, Clock: clock, Tokens: tokens, Auth: svc, Teams: teams, Tasks: tasks,
		Cache: listCache, Redis: rc, router: openAPIRouter(t), client: srv.Client()}
}

// Remote — клиент уже запущенного приложения (например, стека Docker Compose) по baseURL. Ответы
// сверяются с OpenAPI так же, как в API-тестах; базы и Redis у клиента нет.
func Remote(t *testing.T, baseURL string) *APIServer {
	t.Helper()
	return &APIServer{T: t, URL: strings.TrimRight(baseURL, "/"), router: openAPIRouter(t), client: &http.Client{Timeout: 30 * time.Second}}
}

func openAPIRouter(t *testing.T) routers.Router {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData(api.OpenAPISpec)
	if err != nil {
		t.Fatalf("openapi: %v", err)
	}
	// Хост httptest-сервера случаен: сопоставляем маршруты только по пути.
	doc.Servers = nil
	r, err := legacy.NewRouter(doc)
	if err != nil {
		t.Fatalf("openapi router: %v", err)
	}
	return r
}

// Response — ответ тестового запроса.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON декодирует тело ответа в v.
func (r Response) JSON(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.Body, v); err != nil {
		t.Fatalf("тело ответа не JSON: %v\n%s", err, r.Body)
	}
}

// Map возвращает тело как объект.
func (r Response) Map(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	r.JSON(t, &m)
	return m
}

// ErrorCode возвращает code из единого формата ошибки.
func (r Response) ErrorCode(t *testing.T) string {
	t.Helper()
	code, _ := r.Map(t)["code"].(string)
	return code
}

// Request описывает запрос. Body: nil, string/[]byte (как есть) или значение для json.Marshal.
type Request struct {
	Method string
	Path   string // с query, например /api/v1/tasks?team_id=...
	Token  string
	Body   any
	Header http.Header
	// SkipOpenAPI отключает проверку ответа по контракту (например, для намеренно невалидных маршрутов).
	SkipOpenAPI bool
}

// Do выполняет запрос и проверяет, что ответ соответствует api/openapi.yaml: описанный статус,
// заголовки и схема тела. Нарушение контракта проваливает тест.
func (s *APIServer) Do(req Request) Response {
	s.T.Helper()
	var body io.Reader
	switch b := req.Body.(type) {
	case nil:
	case string:
		body = bytes.NewReader([]byte(b))
	case []byte:
		body = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			s.T.Fatalf("marshal body: %v", err)
		}
		body = bytes.NewReader(raw)
	}
	hr, err := http.NewRequest(req.Method, s.URL+req.Path, body)
	if err != nil {
		s.T.Fatalf("new request: %v", err)
	}
	for k, vs := range req.Header {
		for _, v := range vs {
			hr.Header.Add(k, v)
		}
	}
	if req.Token != "" {
		hr.Header.Set("Authorization", "Bearer "+req.Token)
	}
	if body != nil && hr.Header.Get("Content-Type") == "" {
		hr.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.client.Do(hr)
	if err != nil {
		s.T.Fatalf("http: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		s.T.Fatalf("read body: %v", err)
	}
	out := Response{Status: resp.StatusCode, Header: resp.Header, Body: raw}
	if !req.SkipOpenAPI {
		s.validateAgainstSpec(hr, out)
	}
	return out
}

func (s *APIServer) validateAgainstSpec(hr *http.Request, resp Response) {
	s.T.Helper()
	route, pathParams, err := s.router.FindRoute(hr)
	if err != nil {
		s.T.Errorf("маршрут %s %s не описан в OpenAPI: %v", hr.Method, hr.URL.Path, err)
		return
	}
	in := &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request:    hr,
			PathParams: pathParams,
			Route:      route,
			Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
		},
		Status:  resp.Status,
		Header:  resp.Header,
		Options: &openapi3filter.Options{IncludeResponseStatus: true},
	}
	in.SetBodyBytes(resp.Body)
	if err := openapi3filter.ValidateResponse(context.Background(), in); err != nil {
		s.T.Errorf("ответ %s %s (HTTP %d) нарушает OpenAPI: %v\nтело: %s", hr.Method, hr.URL.Path, resp.Status, err, resp.Body)
	}
}

// Register регистрирует пользователя и возвращает его ID.
func (s *APIServer) Register(email, password, name string) string {
	s.T.Helper()
	r := s.Do(Request{Method: "POST", Path: "/api/v1/register",
		Body: map[string]string{"email": email, "password": password, "name": name}})
	if r.Status != http.StatusCreated {
		s.T.Fatalf("register %s: HTTP %d: %s", email, r.Status, r.Body)
	}
	id, _ := r.Map(s.T)["id"].(string)
	return id
}

// Login возвращает access token.
func (s *APIServer) Login(email, password string) string {
	s.T.Helper()
	r := s.Do(Request{Method: "POST", Path: "/api/v1/login", Body: map[string]string{"email": email, "password": password}})
	if r.Status != http.StatusOK {
		s.T.Fatalf("login %s: HTTP %d: %s", email, r.Status, r.Body)
	}
	tok, _ := r.Map(s.T)["access_token"].(string)
	return tok
}

// RegisterAndLogin регистрирует пользователя и возвращает его ID и токен.
func (s *APIServer) RegisterAndLogin(email string) (id, token string) {
	s.T.Helper()
	const pw = "password-for-tests"
	id = s.Register(email, pw, "User "+email)
	return id, s.Login(email, pw)
}
