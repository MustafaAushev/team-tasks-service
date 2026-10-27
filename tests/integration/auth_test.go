//go:build integration

package integration

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/secunda/team-tasks-service/internal/auth"
	"github.com/secunda/team-tasks-service/internal/platform"
	mysqlstore "github.com/secunda/team-tasks-service/internal/storage/mysql"
	"github.com/secunda/team-tasks-service/internal/testutil"
)

type failingIDs struct{}

func (failingIDs) NewID() (uuid.UUID, error) { return uuid.Nil, errors.New("entropy exhausted") }

func newAuth(t *testing.T, ids platform.IDGenerator) (*auth.Service, *auth.TokenManager, *sql.DB) {
	t.Helper()
	db, cfg := testutil.MySQL(t)
	clock := platform.SystemClock{}
	tm := auth.NewTokenManager(cfg.JWTSecret, cfg.JWTIssuer, cfg.JWTAudience, cfg.JWTTTL, clock)
	svc, err := auth.NewService(mysqlstore.UserStore{DB: db}, tm, ids, clock, cfg.BcryptCost)
	if err != nil {
		t.Fatal(err)
	}
	return svc, tm, db
}

func countUsers(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRegisterPersistsNormalizedUserInMySQL(t *testing.T) {
	svc, tm, db := newAuth(t, platform.UUIDv7Generator{})
	ctx := context.Background()

	u, err := svc.Register(ctx, auth.RegisterInput{Email: "  Ann@Example.COM ", Password: "password1", Name: " Ann "})
	if err != nil {
		t.Fatal(err)
	}
	if n := countUsers(t, db); n != 1 {
		t.Fatalf("пользователей: %d", n)
	}
	if u.ID.Version() != 7 || u.Email != "ann@example.com" {
		t.Fatalf("пользователь: %+v", u)
	}

	var rawID []byte
	var storedEmail string
	if err := db.QueryRow(`SELECT id, email FROM users`).Scan(&rawID, &storedEmail); err != nil {
		t.Fatal(err)
	}
	if string(rawID) != string(u.ID[:]) || storedEmail != "ann@example.com" {
		t.Fatalf("в БД: id=% x email=%q", rawID, storedEmail)
	}

	res, err := svc.Login(ctx, "ANN@example.com", "password1")
	if err != nil {
		t.Fatalf("вход сразу после регистрации: %v", err)
	}
	if res.User.ID != u.ID || res.User.Name != " Ann " {
		t.Fatalf("данные из БД: %+v", res.User)
	}
	if res.User.CreatedAt.Location() != time.UTC {
		t.Errorf("created_at должен быть в UTC: %v", res.User.CreatedAt.Location())
	}
	if bcrypt.CompareHashAndPassword([]byte(res.User.PasswordHash), []byte("password1")) != nil {
		t.Error("в БД лежит не bcrypt-хеш пароля")
	}
	if res.User.PasswordHash == "password1" {
		t.Error("пароль сохранён открытым текстом")
	}
	if sub, err := tm.Verify(res.Token); err != nil || sub != u.ID {
		t.Fatalf("sub токена: %v %v", sub, err)
	}
}

func TestRegisterDuplicateNormalizedEmailInMySQL(t *testing.T) {
	svc, _, db := newAuth(t, platform.UUIDv7Generator{})
	ctx := context.Background()

	if _, err := svc.Register(ctx, auth.RegisterInput{Email: "dup@example.com", Password: "password1", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	for _, email := range []string{"DUP@example.com", "  dup@example.com  ", "Dup@Example.Com"} {
		_, err := svc.Register(ctx, auth.RegisterInput{Email: email, Password: "password1", Name: "B"})
		if !errors.Is(err, auth.ErrEmailExists) {
			t.Errorf("%q: ожидался ErrEmailExists, получено %v", email, err)
		}
	}
	if n := countUsers(t, db); n != 1 {
		t.Fatalf("дубликаты создали записи: %d", n)
	}
}

// Уникальность обеспечивает индекс, а не предварительный SELECT: одновременные запросы не создают дубликат.
func TestRegisterConcurrentSameEmailCreatesOneUser(t *testing.T) {
	svc, _, db := newAuth(t, platform.UUIDv7Generator{})
	const workers = 12

	var (
		wg      sync.WaitGroup
		start   = make(chan struct{})
		results = make(chan error, workers)
	)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Register(context.Background(), auth.RegisterInput{Email: "race@example.com", Password: "password1", Name: "R"})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	var ok, dup int
	for err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, auth.ErrEmailExists):
			dup++
		default:
			t.Errorf("неожиданная ошибка: %v", err)
		}
	}
	if ok != 1 || dup != workers-1 {
		t.Fatalf("успехов %d, дубликатов %d; ожидалось 1 и %d", ok, dup, workers-1)
	}
	if n := countUsers(t, db); n != 1 {
		t.Fatalf("записей в БД: %d", n)
	}
}

func TestRegisterIDGenerationFailureLeavesNoRowsInMySQL(t *testing.T) {
	svc, _, db := newAuth(t, failingIDs{})
	if _, err := svc.Register(context.Background(), auth.RegisterInput{Email: "a@example.com", Password: "password1", Name: "A"}); err == nil {
		t.Fatal("ожидалась ошибка генерации ID")
	}
	if n := countUsers(t, db); n != 0 {
		t.Fatalf("частичная запись при ошибке ID: %d строк", n)
	}
}

func TestLoginUnknownAndWrongPasswordAreIndistinguishableInMySQL(t *testing.T) {
	svc, _, _ := newAuth(t, platform.UUIDv7Generator{})
	ctx := context.Background()
	if _, err := svc.Register(ctx, auth.RegisterInput{Email: "known@example.com", Password: "password1", Name: "K"}); err != nil {
		t.Fatal(err)
	}
	_, e1 := svc.Login(ctx, "unknown@example.com", "password1")
	_, e2 := svc.Login(ctx, "known@example.com", "wrong-password")
	if !errors.Is(e1, auth.ErrInvalidCredentials) || !errors.Is(e2, auth.ErrInvalidCredentials) || e1.Error() != e2.Error() {
		t.Fatalf("ошибки различимы: %v / %v", e1, e2)
	}
}
