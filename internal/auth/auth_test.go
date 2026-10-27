package auth_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/secunda/team-tasks-service/internal/auth"
	"github.com/secunda/team-tasks-service/internal/platform"
)

type memStore struct {
	mu      sync.Mutex
	byEmail map[string]auth.User
	creates int
}

func newMemStore() *memStore { return &memStore{byEmail: map[string]auth.User{}} }

func (m *memStore) CreateUser(_ context.Context, u auth.User) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.creates++
	if _, ok := m.byEmail[u.Email]; ok {
		return auth.ErrEmailExists
	}
	m.byEmail[u.Email] = u
	return nil
}

func (m *memStore) FindByEmail(_ context.Context, email string) (auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byEmail[email]
	if !ok {
		return auth.User{}, auth.ErrUserNotFound
	}
	return u, nil
}

type failingIDs struct{}

func (failingIDs) NewID() (uuid.UUID, error) { return uuid.Nil, errors.New("entropy exhausted") }

var (
	secret = []byte("unit-test-secret-unit-test-secret-0123456789")
	now    = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
)

func newService(t *testing.T, store auth.Store, ids platform.IDGenerator, clock platform.Clock) (*auth.Service, *auth.TokenManager) {
	t.Helper()
	tm := auth.NewTokenManager(secret, "iss", "aud", time.Hour, clock)
	svc, err := auth.NewService(store, tm, ids, clock, bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return svc, tm
}

func validInput() auth.RegisterInput {
	return auth.RegisterInput{Email: "ann@example.com", Password: "password1", Name: "Ann"}
}

func fieldsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	var verr *platform.ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("ожидалась ValidationError, получено %v", err)
	}
	return verr.Fields
}

func TestRegisterCreatesFirstUser(t *testing.T) {
	store := newMemStore()
	clock := &platform.FixedClock{T: now}
	svc, _ := newService(t, store, platform.UUIDv7Generator{}, clock)

	u, err := svc.Register(context.Background(), auth.RegisterInput{Email: "  Ann@Example.COM ", Password: "password1", Name: "Ann"})
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "ann@example.com" {
		t.Errorf("email не нормализован: %q", u.Email)
	}
	if u.ID.Version() != 7 {
		t.Errorf("ожидался UUIDv7, версия %d", u.ID.Version())
	}
	if !u.CreatedAt.Equal(now) {
		t.Errorf("created_at: %v", u.CreatedAt)
	}
	if u.PasswordHash == "" || strings.Contains(u.PasswordHash, "password1") {
		t.Error("пароль должен храниться только в виде хеша")
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte("password1")) != nil {
		t.Error("хеш не соответствует паролю")
	}
}

func TestRegisterDuplicateNormalizedEmail(t *testing.T) {
	store := newMemStore()
	svc, _ := newService(t, store, platform.UUIDv7Generator{}, &platform.FixedClock{T: now})
	if _, err := svc.Register(context.Background(), validInput()); err != nil {
		t.Fatal(err)
	}
	dup := validInput()
	dup.Email = "  ANN@example.com  "
	if _, err := svc.Register(context.Background(), dup); !errors.Is(err, auth.ErrEmailExists) {
		t.Fatalf("ожидался ErrEmailExists, получено %v", err)
	}
	if len(store.byEmail) != 1 {
		t.Fatalf("пользователей: %d", len(store.byEmail))
	}
}

func TestRegisterValidation(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	tests := []struct {
		name  string
		in    auth.RegisterInput
		field string // пусто — ожидается успех
	}{
		{"пароль 8 символов", auth.RegisterInput{Email: "a@b.co", Password: "12345678", Name: "N"}, ""},
		{"пароль 7 символов", auth.RegisterInput{Email: "a@b.co", Password: "1234567", Name: "N"}, "password"},
		{"пароль ровно 72 байта", auth.RegisterInput{Email: "a@b.co", Password: long(72), Name: "N"}, ""},
		{"пароль 73 байта", auth.RegisterInput{Email: "a@b.co", Password: long(73), Name: "N"}, "password"},
		{"36 двухбайтовых символов = 72 байта", auth.RegisterInput{Email: "a@b.co", Password: strings.Repeat("я", 36), Name: "N"}, ""},
		{"37 двухбайтовых символов = 74 байта", auth.RegisterInput{Email: "a@b.co", Password: strings.Repeat("я", 37), Name: "N"}, "password"},
		{"8 двухбайтовых символов", auth.RegisterInput{Email: "a@b.co", Password: strings.Repeat("я", 8), Name: "N"}, ""},
		{"пустой пароль", auth.RegisterInput{Email: "a@b.co", Password: "", Name: "N"}, "password"},
		{"пустой email", auth.RegisterInput{Email: "  ", Password: "password1", Name: "N"}, "email"},
		{"email без домена", auth.RegisterInput{Email: "a@", Password: "password1", Name: "N"}, "email"},
		{"email без точки в домене", auth.RegisterInput{Email: "a@localhost", Password: "password1", Name: "N"}, "email"},
		{"email с именем", auth.RegisterInput{Email: "Ann <a@b.co>", Password: "password1", Name: "N"}, "email"},
		{"email 254 символа", auth.RegisterInput{Email: long(64) + "@" + long(63) + "." + long(63) + "." + long(57) + ".com", Password: "password1", Name: "N"}, ""},
		{"email 255 символов", auth.RegisterInput{Email: long(64) + "@" + long(63) + "." + long(63) + "." + long(58) + ".com", Password: "password1", Name: "N"}, "email"},
		{"имя из пробелов", auth.RegisterInput{Email: "a@b.co", Password: "password1", Name: "   "}, "name"},
		{"имя пустое", auth.RegisterInput{Email: "a@b.co", Password: "password1", Name: ""}, "name"},
		{"имя 120 символов", auth.RegisterInput{Email: "a@b.co", Password: "password1", Name: strings.Repeat("я", 120)}, ""},
		{"имя 121 символ", auth.RegisterInput{Email: "a@b.co", Password: "password1", Name: strings.Repeat("я", 121)}, "name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newMemStore()
			svc, _ := newService(t, store, platform.UUIDv7Generator{}, &platform.FixedClock{T: now})
			_, err := svc.Register(context.Background(), tt.in)
			if tt.field == "" {
				if err != nil {
					t.Fatalf("ожидался успех: %v", err)
				}
				return
			}
			if _, ok := fieldsOf(t, err)[tt.field]; !ok {
				t.Fatalf("ожидалась ошибка поля %s: %v", tt.field, err)
			}
			if store.creates != 0 {
				t.Fatal("при ошибке валидации запись не должна выполняться")
			}
		})
	}
}

func TestRegisterIDGenerationFailureWritesNothing(t *testing.T) {
	store := newMemStore()
	svc, _ := newService(t, store, failingIDs{}, &platform.FixedClock{T: now})
	if _, err := svc.Register(context.Background(), validInput()); err == nil {
		t.Fatal("ожидалась ошибка генерации ID")
	}
	if store.creates != 0 || len(store.byEmail) != 0 {
		t.Fatalf("частичная запись при ошибке ID: creates=%d", store.creates)
	}
}

func TestPasswordIsNeverTruncated(t *testing.T) {
	store := newMemStore()
	svc, _ := newService(t, store, platform.UUIDv7Generator{}, &platform.FixedClock{T: now})
	pw := strings.Repeat("a", 71) + "X"
	in := validInput()
	in.Password = pw
	if _, err := svc.Register(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(context.Background(), in.Email, pw); err != nil {
		t.Fatalf("вход с полным паролем: %v", err)
	}
	// Отличие только в последнем (72-м) байте не должно игнорироваться.
	if _, err := svc.Login(context.Background(), in.Email, strings.Repeat("a", 71)+"Y"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("72-й байт обязан учитываться: %v", err)
	}
	// Пароль длиннее 72 байт не совпадает с усечённым.
	if _, err := svc.Login(context.Background(), in.Email, pw+"zzz"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("длинный пароль не должен усекаться: %v", err)
	}
}

func TestLoginSuccessAndUniformFailure(t *testing.T) {
	store := newMemStore()
	clock := &platform.FixedClock{T: now}
	svc, tm := newService(t, store, platform.UUIDv7Generator{}, clock)
	u, err := svc.Register(context.Background(), validInput())
	if err != nil {
		t.Fatal(err)
	}

	res, err := svc.Login(context.Background(), "  ANN@example.com ", "password1")
	if err != nil {
		t.Fatal(err)
	}
	if res.ExpiresIn != 3600 || res.User.ID != u.ID {
		t.Errorf("результат входа: %+v", res)
	}
	sub, err := tm.Verify(res.Token)
	if err != nil || sub != u.ID {
		t.Fatalf("sub токена: %v %v", sub, err)
	}

	_, errUnknown := svc.Login(context.Background(), "nobody@example.com", "password1")
	_, errWrongPW := svc.Login(context.Background(), "ann@example.com", "wrong-password")
	if !errors.Is(errUnknown, auth.ErrInvalidCredentials) || !errors.Is(errWrongPW, auth.ErrInvalidCredentials) {
		t.Fatalf("обе ошибки должны быть ErrInvalidCredentials: %v / %v", errUnknown, errWrongPW)
	}
	if errUnknown.Error() != errWrongPW.Error() {
		t.Error("тексты ошибок обязаны совпадать")
	}
}

func TestLoginRequiresFields(t *testing.T) {
	svc, _ := newService(t, newMemStore(), platform.UUIDv7Generator{}, &platform.FixedClock{T: now})
	_, err := svc.Login(context.Background(), " ", "")
	f := fieldsOf(t, err)
	if f["email"] == "" || f["password"] == "" {
		t.Fatalf("поля: %v", f)
	}
}

func TestTokenLifetimeAndExpiry(t *testing.T) {
	clock := &platform.FixedClock{T: now}
	tm := auth.NewTokenManager(secret, "iss", "aud", 15*time.Minute, clock)
	id, _ := platform.UUIDv7Generator{}.NewID()
	token, exp, err := tm.Issue(id)
	if err != nil || exp != 900 {
		t.Fatalf("issue: %v exp=%d", err, exp)
	}
	if _, err := tm.Verify(token); err != nil {
		t.Fatalf("свежий токен: %v", err)
	}
	clock.T = now.Add(15*time.Minute - time.Second)
	if _, err := tm.Verify(token); err != nil {
		t.Fatalf("за секунду до истечения: %v", err)
	}
	clock.T = now.Add(15*time.Minute + time.Second)
	if _, err := tm.Verify(token); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("истёкший токен должен отклоняться: %v", err)
	}
}

func TestTokenClaims(t *testing.T) {
	clock := &platform.FixedClock{T: now}
	tm := auth.NewTokenManager(secret, "iss", "aud", time.Hour, clock)
	id, _ := platform.UUIDv7Generator{}.NewID()
	token, _, _ := tm.Issue(id)

	var claims jwt.MapClaims
	parsed, _, err := jwt.NewParser().ParseUnverified(token, &claims)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Method.Alg() != "HS256" {
		t.Errorf("алгоритм %s", parsed.Method.Alg())
	}
	if claims["sub"] != id.String() {
		t.Errorf("sub=%v", claims["sub"])
	}
	for _, k := range []string{"iss", "aud", "iat", "exp", "sub"} {
		if _, ok := claims[k]; !ok {
			t.Errorf("нет claim %s", k)
		}
	}
	for _, k := range []string{"role", "roles", "team_roles", "teams"} {
		if _, ok := claims[k]; ok {
			t.Errorf("в токене не должно быть командных ролей: %s", k)
		}
	}
}

func signWith(t *testing.T, method jwt.SigningMethod, key any, claims jwt.Claims) string {
	t.Helper()
	s, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTokenRejectsInvalid(t *testing.T) {
	clock := &platform.FixedClock{T: now}
	tm := auth.NewTokenManager(secret, "iss", "aud", time.Hour, clock)
	id, _ := platform.UUIDv7Generator{}.NewID()
	v4 := uuid.New()

	good := func(sub string) jwt.RegisteredClaims {
		return jwt.RegisteredClaims{
			Subject: sub, Issuer: "iss", Audience: jwt.ClaimStrings{"aud"},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		}
	}
	valid, _, _ := tm.Issue(id)
	otherKey := []byte("another-secret-another-secret-0123456789")

	cases := map[string]string{
		"пусто":            "",
		"мусор":            "not-a-jwt",
		"чужая подпись":    signWith(t, jwt.SigningMethodHS256, otherKey, good(id.String())),
		"алгоритм HS384":   signWith(t, jwt.SigningMethodHS384, secret, good(id.String())),
		"алгоритм HS512":   signWith(t, jwt.SigningMethodHS512, secret, good(id.String())),
		"alg none":         signWith(t, jwt.SigningMethodNone, jwt.UnsafeAllowNoneSignatureType, good(id.String())),
		"sub не UUID":      signWith(t, jwt.SigningMethodHS256, secret, good("42")),
		"sub пуст":         signWith(t, jwt.SigningMethodHS256, secret, good("")),
		"sub UUID v4":      signWith(t, jwt.SigningMethodHS256, secret, good(v4.String())),
		"sub nil UUID":     signWith(t, jwt.SigningMethodHS256, secret, good(uuid.Nil.String())),
		"другой issuer":    signWith(t, jwt.SigningMethodHS256, secret, func() jwt.RegisteredClaims { c := good(id.String()); c.Issuer = "evil"; return c }()),
		"другая audience":  signWith(t, jwt.SigningMethodHS256, secret, func() jwt.RegisteredClaims { c := good(id.String()); c.Audience = jwt.ClaimStrings{"other"}; return c }()),
		"без exp":          signWith(t, jwt.SigningMethodHS256, secret, func() jwt.RegisteredClaims { c := good(id.String()); c.ExpiresAt = nil; return c }()),
		"подпись обрезана": valid[:len(valid)-4],
		"payload подменён": strings.Join([]string{strings.Split(valid, ".")[0], strings.Split(signWith(t, jwt.SigningMethodHS256, otherKey, good(v4.String())), ".")[1], strings.Split(valid, ".")[2]}, "."),
	}
	for name, tok := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := tm.Verify(tok); !errors.Is(err, auth.ErrInvalidToken) {
				t.Fatalf("токен должен быть отклонён, получено %v", err)
			}
		})
	}
	if _, err := tm.Verify(valid); err != nil {
		t.Fatalf("контрольный валидный токен: %v", err)
	}
}
