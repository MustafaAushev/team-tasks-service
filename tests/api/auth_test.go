//go:build api

package api

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/config"
	"github.com/secunda/team-tasks-service/internal/httpapi"
	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/testutil"
)

func register(s *testutil.APIServer, email, password, name string) testutil.Response {
	s.T.Helper()
	return s.Do(testutil.Request{Method: "POST", Path: "/api/v1/register",
		Body: map[string]string{"email": email, "password": password, "name": name}})
}

func login(s *testutil.APIServer, email, password string) testutil.Response {
	s.T.Helper()
	return s.Do(testutil.Request{Method: "POST", Path: "/api/v1/login",
		Body: map[string]string{"email": email, "password": password}})
}

// jwtPayload декодирует claims без проверки подписи: тест смотрит, что именно выдал сервер.
func jwtPayload(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("токен не из трёх частей: %q", token)
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// assertNoSecrets: ни пароль, ни хеш не должны появляться в ответе.
func assertNoSecrets(t *testing.T, body []byte, password string) {
	t.Helper()
	text := string(body)
	for _, bad := range []string{`"password`, "password_hash", "$2a$", "$2b$", "$2y$", password} {
		if strings.Contains(text, bad) {
			t.Errorf("ответ раскрывает секрет (%q): %s", bad, text)
		}
	}
}

func TestRegisterThenLoginImmediately(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{})
	const pw = "correct horse battery"

	r := register(s, "  Ann@Example.COM ", pw, "Ann")
	if r.Status != http.StatusCreated {
		t.Fatalf("register: HTTP %d: %s", r.Status, r.Body)
	}
	u := r.Map(t)
	if len(u) != 4 {
		t.Errorf("публичный профиль должен содержать ровно id, email, name, created_at: %v", u)
	}
	id, _ := u["id"].(string)
	parsed, err := platform.ParseID(id)
	if err != nil || id != strings.ToLower(id) || parsed.Version() != 7 {
		t.Fatalf("id профиля не UUIDv7 в нижнем регистре: %q (%v)", id, err)
	}
	if u["email"] != "ann@example.com" || u["name"] != "Ann" {
		t.Errorf("профиль: %v", u)
	}
	if ts, _ := u["created_at"].(string); !strings.HasSuffix(ts, "Z") {
		t.Errorf("created_at должен быть в UTC: %q", ts)
	}
	assertNoSecrets(t, r.Body, pw)

	// Вход возможен сразу, без почтового подтверждения; email принимается в любом регистре.
	l := login(s, "ANN@example.com", pw)
	if l.Status != http.StatusOK {
		t.Fatalf("login: HTTP %d: %s", l.Status, l.Body)
	}
	lm := l.Map(t)
	if lm["token_type"] != "Bearer" || lm["expires_in"] != float64(3600) {
		t.Errorf("ответ входа: %v", lm)
	}
	if user, _ := lm["user"].(map[string]any); user["id"] != id {
		t.Errorf("профиль во входе: %v", lm["user"])
	}
	assertNoSecrets(t, l.Body, pw)

	claims := jwtPayload(t, lm["access_token"].(string))
	if claims["sub"] != id {
		t.Errorf("sub=%v, ожидался %s", claims["sub"], id)
	}
	if claims["iss"] != s.Config.JWTIssuer {
		t.Errorf("iss=%v", claims["iss"])
	}
	if aud, _ := claims["aud"].([]any); len(aud) != 1 || aud[0] != s.Config.JWTAudience {
		t.Errorf("aud=%v", claims["aud"])
	}
	if exp, iat := claims["exp"].(float64), claims["iat"].(float64); exp-iat != 3600 {
		t.Errorf("срок жизни %v с, ожидалось 3600", exp-iat)
	}
	for _, k := range []string{"role", "roles", "team_roles", "teams"} {
		if _, ok := claims[k]; ok {
			t.Errorf("в JWT не должно быть командных ролей (%s)", k)
		}
	}
}

func TestRegisterDuplicateEmailIs409(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{})
	if r := register(s, "dup@example.com", "password1", "First"); r.Status != 201 {
		t.Fatalf("первая регистрация: HTTP %d", r.Status)
	}
	for _, email := range []string{"dup@example.com", "DUP@EXAMPLE.COM", "  dup@example.com  "} {
		r := register(s, email, "another-pass", "Second")
		if r.Status != http.StatusConflict || r.ErrorCode(t) != "email_already_exists" {
			t.Errorf("%q: HTTP %d code=%s", email, r.Status, r.ErrorCode(t))
		}
	}
	// Исходная учётная запись не пострадала, второй пароль не принят.
	if r := login(s, "dup@example.com", "password1"); r.Status != 200 {
		t.Errorf("вход в исходную запись: HTTP %d", r.Status)
	}
	if r := login(s, "dup@example.com", "another-pass"); r.Status != 401 {
		t.Errorf("пароль дубликата не должен подходить: HTTP %d", r.Status)
	}
}

func TestRegisterValidationErrors(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{})
	long := func(n int) string { return strings.Repeat("a", n) }

	cases := []struct {
		name  string
		email string
		pw    string
		uname string
		field string // пусто — ожидается успех
	}{
		{"пароль 8 символов", "p8@example.com", "12345678", "N", ""},
		{"пароль 7 символов", "p7@example.com", "1234567", "N", "password"},
		{"пароль ровно 72 байта", "p72@example.com", long(72), "N", ""},
		{"пароль 73 байта", "p73@example.com", long(73), "N", "password"},
		{"36 двухбайтовых символов (72 байта)", "u36@example.com", strings.Repeat("я", 36), "N", ""},
		{"37 двухбайтовых символов (74 байта)", "u37@example.com", strings.Repeat("я", 37), "N", "password"},
		{"пустой пароль", "empty@example.com", "", "N", "password"},
		{"некорректный email", "not-an-email", "password1", "N", "email"},
		{"email без точки в домене", "a@localhost", "password1", "N", "email"},
		{"пустой email", "  ", "password1", "N", "email"},
		{"имя из пробелов", "n1@example.com", "password1", "   ", "name"},
		{"имя 120 символов", "n120@example.com", "password1", strings.Repeat("я", 120), ""},
		{"имя 121 символ", "n121@example.com", "password1", strings.Repeat("я", 121), "name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := register(s, c.email, c.pw, c.uname)
			if c.field == "" {
				if r.Status != http.StatusCreated {
					t.Fatalf("ожидался успех: HTTP %d %s", r.Status, r.Body)
				}
				return
			}
			if r.Status != http.StatusUnprocessableEntity || r.ErrorCode(t) != "validation_error" {
				t.Fatalf("HTTP %d code=%s: %s", r.Status, r.ErrorCode(t), r.Body)
			}
			details, _ := r.Map(t)["details"].(map[string]any)
			fields, _ := details["fields"].(map[string]any)
			if _, ok := fields[c.field]; !ok {
				t.Fatalf("ожидалась ошибка поля %q: %s", c.field, r.Body)
			}
			// Невалидная регистрация не создаёт пользователя.
			if l := login(s, strings.TrimSpace(c.email), c.pw); l.Status == http.StatusOK {
				t.Fatal("пользователь создан несмотря на ошибку валидации")
			}
		})
	}

	// Пароль 72 байта не усекается: отличие в последнем байте различимо.
	if l := login(s, "p72@example.com", long(71)+"b"); l.Status != 401 {
		t.Errorf("отличающийся 72-й байт должен давать 401, получено %d", l.Status)
	}
}

func TestLoginFailuresAreUniform(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{})
	if r := register(s, "known@example.com", "password1", "K"); r.Status != 201 {
		t.Fatal(string(r.Body))
	}

	unknown := login(s, "ghost@example.com", "password1")
	wrong := login(s, "known@example.com", "wrong-password")
	if unknown.Status != 401 || wrong.Status != 401 {
		t.Fatalf("статусы %d/%d", unknown.Status, wrong.Status)
	}
	a, b := unknown.Map(t), wrong.Map(t)
	delete(a, "request_id")
	delete(b, "request_id")
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) || a["code"] != "invalid_credentials" {
		t.Fatalf("ответы различимы:\n%s\n%s", ja, jb)
	}

	// Поля обязательны, а лишние отклоняются.
	if r := s.Do(testutil.Request{Method: "POST", Path: "/api/v1/login", Body: `{"email":"known@example.com"}`}); r.Status != 422 {
		t.Errorf("без пароля: HTTP %d", r.Status)
	}
	if r := s.Do(testutil.Request{Method: "POST", Path: "/api/v1/login", Body: `{"email":"a@b.co","password":"x","remember":true}`}); r.Status != 422 {
		t.Errorf("лишнее поле: HTTP %d", r.Status)
	}
}

// protectedStatus выполняет запрос к защищённому маршруту; нас интересует только прохождение аутентификации.
func protectedStatus(s *testutil.APIServer, authHeader string) int {
	s.T.Helper()
	h := http.Header{}
	if authHeader != "" {
		h.Set("Authorization", authHeader)
	}
	return s.Do(testutil.Request{Method: "GET", Path: "/api/v1/teams", Header: h, SkipOpenAPI: true}).Status
}

func TestTokenLifetimeIsConfigurableAndEnforced(t *testing.T) {
	clock := &platform.FixedClock{T: time.Now().UTC()}
	s := testutil.StartAPI(t, testutil.APIOptions{
		Clock:     clock,
		Configure: func(c *config.Config) { c.JWTTTL = 90 * time.Second },
	})
	s.Register("ttl@example.com", "password1", "T")
	l := login(s, "ttl@example.com", "password1")
	if l.Status != 200 || l.Map(t)["expires_in"] != float64(90) {
		t.Fatalf("expires_in: %s", l.Body)
	}
	token := l.Map(t)["access_token"].(string)
	bearer := "Bearer " + token

	if st := protectedStatus(s, bearer); st == http.StatusUnauthorized {
		t.Fatalf("свежий токен отклонён")
	}
	clock.Advance(89 * time.Second)
	if st := protectedStatus(s, bearer); st == http.StatusUnauthorized {
		t.Fatalf("токен за секунду до истечения отклонён")
	}
	clock.Advance(2 * time.Second)
	if st := protectedStatus(s, bearer); st != http.StatusUnauthorized {
		t.Fatalf("истёкший токен должен давать 401, получено %d", st)
	}
}

func TestInvalidTokensAreRejectedByRealStack(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{})
	id, token := s.RegisterAndLogin("tokens@example.com")
	now := time.Now().UTC()

	claims := func(sub string) jwt.RegisteredClaims {
		return jwt.RegisteredClaims{
			Subject: sub, Issuer: s.Config.JWTIssuer, Audience: jwt.ClaimStrings{s.Config.JWTAudience},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		}
	}
	sign := func(m jwt.SigningMethod, key any, c jwt.RegisteredClaims) string {
		out, err := jwt.NewWithClaims(m, c).SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return "Bearer " + out
	}
	secret := s.Config.JWTSecret
	other := []byte("some-other-secret-some-other-secret-0123456")

	// Контроль: корректный токен проходит аутентификацию.
	if st := protectedStatus(s, "Bearer "+token); st == http.StatusUnauthorized {
		t.Fatal("корректный токен отклонён")
	}

	tampered := token[:len(token)-3] + "AAA"
	cases := map[string]string{
		"нет заголовка":    "",
		"подпись изменена": "Bearer " + tampered,
		"чужой секрет":     sign(jwt.SigningMethodHS256, other, claims(id)),
		"alg none":         sign(jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, claims(id)),
		"alg HS384":        sign(jwt.SigningMethodHS384, secret, claims(id)),
		"sub не UUID":      sign(jwt.SigningMethodHS256, secret, claims("42")),
		"sub UUID v4":      sign(jwt.SigningMethodHS256, secret, claims(uuid.NewString())),
		"sub nil UUID":     sign(jwt.SigningMethodHS256, secret, claims(uuid.Nil.String())),
		"другой issuer":    sign(jwt.SigningMethodHS256, secret, func() jwt.RegisteredClaims { c := claims(id); c.Issuer = "evil"; return c }()),
		"другая audience":  sign(jwt.SigningMethodHS256, secret, func() jwt.RegisteredClaims { c := claims(id); c.Audience = jwt.ClaimStrings{"x"}; return c }()),
		"истёкший": sign(jwt.SigningMethodHS256, secret, func() jwt.RegisteredClaims {
			c := claims(id)
			c.ExpiresAt = jwt.NewNumericDate(now.Add(-time.Minute))
			return c
		}()),
		"не Bearer":           "Basic dXNlcjpwYXNz",
		"Bearer без значения": "Bearer",
	}
	for name, header := range cases {
		t.Run(name, func(t *testing.T) {
			h := http.Header{}
			if header != "" {
				h.Set("Authorization", header)
			}
			r := s.Do(testutil.Request{Method: "GET", Path: "/api/v1/teams", Header: h})
			if r.Status != http.StatusUnauthorized || r.ErrorCode(t) != "unauthorized" {
				t.Fatalf("HTTP %d code=%s: %s", r.Status, r.ErrorCode(t), r.Body)
			}
		})
	}
}

// Одна личность, разные роли в командах: роль не хранится в токене и читается из MySQL на каждом
// запросе, поэтому смена роли действует без перевыдачи токена. Роль здесь читает тестовый обработчик;
// боевая проверка прав команд реализуется и тестируется в группе 4.
func TestOneIdentityDifferentRolesWithoutReissuingToken(t *testing.T) {
	var s *testutil.APIServer
	var db *sql.DB
	whoAmI := func(w http.ResponseWriter, r *http.Request) {
		uid, ok := httpapi.UserID(r.Context())
		teamID, _ := httpapi.PathID(r.Context(), "id")
		if !ok {
			http.Error(w, "нет личности", http.StatusInternalServerError)
			return
		}
		var role string
		err := db.QueryRowContext(r.Context(),
			`SELECT role FROM team_members WHERE team_id = ? AND user_id = ?`,
			platform.IDBytes(teamID), platform.IDBytes(uid)).Scan(&role)
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			_, _ = w.Write([]byte(`{"role":null}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"role": role})
	}
	s = testutil.StartAPI(t, testutil.APIOptions{Ops: httpapi.Operations{ListMembers: whoAmI}})
	db = s.DB

	userID, token := s.RegisterAndLogin("multi@example.com")
	teamA, teamB := newID(t), newID(t)
	mustExec(t, db, `INSERT INTO teams (id, name, created_by, created_at) VALUES (?, 'A', ?, NOW(6)), (?, 'B', ?, NOW(6))`,
		platform.IDBytes(teamA), idBytes(t, userID), platform.IDBytes(teamB), idBytes(t, userID))
	mustExec(t, db, `INSERT INTO team_members (team_id, user_id, role, created_at) VALUES (?, ?, 'owner', NOW(6)), (?, ?, 'member', NOW(6))`,
		platform.IDBytes(teamA), idBytes(t, userID), platform.IDBytes(teamB), idBytes(t, userID))

	roleIn := func(team uuid.UUID) any {
		r := s.Do(testutil.Request{Method: "GET", Path: "/api/v1/teams/" + team.String() + "/members", Token: token, SkipOpenAPI: true})
		if r.Status != http.StatusOK {
			t.Fatalf("HTTP %d: %s", r.Status, r.Body)
		}
		return r.Map(t)["role"]
	}
	if roleIn(teamA) != "owner" || roleIn(teamB) != "member" {
		t.Fatal("один токен должен давать разные роли в разных командах")
	}

	// Роль изменилась после выдачи токена — тот же токен видит новую роль.
	mustExec(t, db, `UPDATE team_members SET role = 'admin' WHERE team_id = ? AND user_id = ?`,
		platform.IDBytes(teamB), idBytes(t, userID))
	if got := roleIn(teamB); got != "admin" {
		t.Fatalf("после смены роли: %v", got)
	}
	// Без токена роли не видны вовсе.
	if r := s.Do(testutil.Request{Method: "GET", Path: "/api/v1/teams/" + teamA.String() + "/members", SkipOpenAPI: true}); r.Status != 401 {
		t.Fatalf("без токена: HTTP %d", r.Status)
	}
}

func newID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := platform.UUIDv7Generator{}.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func idBytes(t *testing.T, s string) []byte {
	t.Helper()
	id, err := platform.ParseID(s)
	if err != nil {
		t.Fatal(err)
	}
	return platform.IDBytes(id)
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
