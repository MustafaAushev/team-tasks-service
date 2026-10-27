package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/auth"
	"github.com/secunda/team-tasks-service/internal/platform"
)

type fakeStore struct {
	mu      sync.Mutex
	users   map[string]auth.User
	failErr error // если задана, любая операция хранилища возвращает её
}

func newFakeStore() *fakeStore { return &fakeStore{users: map[string]auth.User{}} }

func (f *fakeStore) CreateUser(_ context.Context, u auth.User) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return f.failErr
	}
	if _, ok := f.users[u.Email]; ok {
		return auth.ErrEmailExists
	}
	f.users[u.Email] = u
	return nil
}

func (f *fakeStore) FindByEmail(_ context.Context, email string) (auth.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return auth.User{}, f.failErr
	}
	u, ok := f.users[email]
	if !ok {
		return auth.User{}, auth.ErrUserNotFound
	}
	return u, nil
}

type harness struct {
	t      *testing.T
	h      http.Handler
	store  *fakeStore
	tokens *auth.TokenManager
	clock  *platform.FixedClock
	logs   *bytes.Buffer
	token  string
	userID uuid.UUID
}

// stubOp отвечает за операции, которые тест не задал: транспортные тесты проверяют маршрутизацию,
// JWT и разбор запроса, а не бизнес-логику.
func stubOp(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"stub": true})
}

// harnessOps собирает операции стенда: регистрация и вход — настоящие (из real, на fakeStore),
// остальные — stubOp; заданные в override заменяют и те, и другие.
func harnessOps(real, override Operations) Operations {
	ops := Operations{Register: real.Register, Login: real.Login}
	v, o := reflect.ValueOf(&ops).Elem(), reflect.ValueOf(override)
	for i := range v.NumField() {
		switch f := o.Field(i); {
		case !f.IsNil():
			v.Field(i).Set(f)
		case v.Field(i).IsNil():
			v.Field(i).Set(reflect.ValueOf(http.HandlerFunc(stubOp)))
		}
	}
	return ops
}

func newHarness(t *testing.T, ops Operations) *harness {
	t.Helper()
	clock := &platform.FixedClock{T: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	store := newFakeStore()
	tokens := auth.NewTokenManager([]byte("httpapi-test-secret-httpapi-test-secret-01"), "iss", "aud", time.Hour, clock)
	svc, err := auth.NewService(store, tokens, platform.UUIDv7Generator{}, clock, 4)
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	logger := platform.NewLogger(logs, -4)
	h := NewHandler(Deps{
		Logger:         logger,
		Tokens:         tokens,
		RequestTimeout: 5 * time.Second,
		Ops:            harnessOps(NewOperations(Services{Logger: logger, Auth: svc}), ops),
		OpenAPI:        []byte("openapi: 3.0.3\n"),
	})
	uid, _ := platform.UUIDv7Generator{}.NewID()
	tok, _, err := tokens.Issue(uid)
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, h: h, store: store, tokens: tokens, clock: clock, logs: logs, token: tok, userID: uid}
}

type rec struct {
	*httptest.ResponseRecorder
}

func (r rec) json() map[string]any {
	var m map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &m); err != nil {
		panic("тело не JSON: " + r.Body.String())
	}
	return m
}

func (r rec) code() string {
	c, _ := r.json()["code"].(string)
	return c
}

func (r rec) fields() map[string]any {
	d, _ := r.json()["details"].(map[string]any)
	f, _ := d["fields"].(map[string]any)
	return f
}

// do отправляет запрос; token "" — без авторизации, "-" — токен по умолчанию.
func (h *harness) do(method, target, token string, body any) rec {
	h.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, _ := json.Marshal(b)
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, rd)
	if token == "-" {
		token = h.token
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, req)
	return rec{w}
}

var errBoom = errors.New("Error 1146 (42S02): Table 'tasks.users' doesn't exist; SELECT id FROM users WHERE email = 'a@b.c'")

func newReq(method, target string, headers map[string]string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func serve(h *harness, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, req)
	return w
}
