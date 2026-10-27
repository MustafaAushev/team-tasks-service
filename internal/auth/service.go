package auth

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/secunda/team-tasks-service/internal/platform"
)

const (
	maxEmailLen    = 254
	minPasswordLen = 8
	maxPasswordLen = 72 // байт UTF-8: ограничение bcrypt, усечение недопустимо
	maxNameLen     = 120
)

var (
	// ErrEmailExists — нормализованный email уже зарегистрирован.
	ErrEmailExists = errors.New("email already exists")
	// ErrInvalidCredentials — неверный email или пароль; ответ одинаков для обоих случаев.
	ErrInvalidCredentials = errors.New("invalid credentials")
	// ErrUserNotFound возвращается хранилищем, когда пользователя нет.
	ErrUserNotFound = errors.New("user not found")
)

// User — учётная запись. Глобальной роли нет.
type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	Name         string
	CreatedAt    time.Time
}

// Store — то, что сервису нужно от хранилища пользователей.
type Store interface {
	// CreateUser вставляет пользователя; при занятом email возвращает ErrEmailExists.
	CreateUser(ctx context.Context, u User) error
	// FindByEmail возвращает пользователя по нормализованному email или ErrUserNotFound.
	FindByEmail(ctx context.Context, email string) (User, error)
}

// Service реализует регистрацию и вход.
type Service struct {
	store     Store
	tokens    *TokenManager
	ids       platform.IDGenerator
	clock     platform.Clock
	cost      int
	dummyHash []byte
}

// NewService готовит сервис; фиктивный хеш нужен, чтобы вход с неизвестным email
// тратил столько же времени, сколько вход с неверным паролем.
func NewService(store Store, tokens *TokenManager, ids platform.IDGenerator, clock platform.Clock, bcryptCost int) (*Service, error) {
	dummy, err := bcrypt.GenerateFromPassword([]byte("dummy-password-for-timing"), bcryptCost)
	if err != nil {
		return nil, fmt.Errorf("prepare dummy hash: %w", err)
	}
	return &Service{store: store, tokens: tokens, ids: ids, clock: clock, cost: bcryptCost, dummyHash: dummy}, nil
}

// NormalizeEmail убирает внешние пробелы и приводит email к нижнему регистру.
func NormalizeEmail(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// RegisterInput — данные регистрации до нормализации.
type RegisterInput struct{ Email, Password, Name string }

// Register создаёт пользователя. Подтверждение email не требуется.
//
// Владение адресом НЕ проверяется: письмо с кодом/ссылкой не отправляется (SMTP в сервисе нет),
// поэтому пользователь может зарегистрировать чужой адрес. Для реальных пользователей перед
// использованием нужно добавить почтовое подтверждение; см. README, «Допущения и ограничения».
// Уникальность гарантирует UNIQUE-индекс по нормализованному email, а не предварительный SELECT:
// два одновременных запроса не создадут дубликат.
func (s *Service) Register(ctx context.Context, in RegisterInput) (User, error) {
	email := NormalizeEmail(in.Email)
	if err := validateRegistration(email, in.Password, in.Name); err != nil {
		return User{}, err
	}

	// ID генерируется до любой записи: ошибка генерации прерывает операцию без частичных данных.
	id, err := s.ids.NewID()
	if err != nil {
		return User{}, fmt.Errorf("register: %w", err)
	}
	// bcrypt не усекает: длина уже проверена (<= 72 байт).
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), s.cost)
	if err != nil {
		return User{}, fmt.Errorf("register: hash password: %w", err)
	}
	u := User{ID: id, Email: email, PasswordHash: string(hash), Name: in.Name, CreatedAt: s.clock.Now().Truncate(time.Microsecond)}
	if err := s.store.CreateUser(ctx, u); err != nil {
		return User{}, err
	}
	return u, nil
}

// LoginResult — результат успешного входа.
type LoginResult struct {
	Token     string
	ExpiresIn int64
	User      User
}

// Login проверяет реквизиты и выдаёт JWT. Неизвестный email и неверный пароль неразличимы.
func (s *Service) Login(ctx context.Context, email, password string) (LoginResult, error) {
	email = NormalizeEmail(email)
	fields := map[string]string{}
	if email == "" {
		fields["email"] = "обязательное поле"
	}
	if password == "" {
		fields["password"] = "обязательное поле"
	}
	if len(fields) > 0 {
		return LoginResult{}, &platform.ValidationError{Fields: fields}
	}

	u, err := s.store.FindByEmail(ctx, email)
	switch {
	case errors.Is(err, ErrUserNotFound):
		// Выравниваем время ответа с веткой «неверный пароль».
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
		return LoginResult{}, ErrInvalidCredentials
	case err != nil:
		return LoginResult{}, err
	}
	if len(password) > maxPasswordLen ||
		bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return LoginResult{}, ErrInvalidCredentials
	}

	token, expiresIn, err := s.tokens.Issue(u.ID)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{Token: token, ExpiresIn: expiresIn, User: u}, nil
}

func validateRegistration(email, password, name string) error {
	fields := map[string]string{}

	switch {
	case email == "":
		fields["email"] = "обязательное поле"
	case len(email) > maxEmailLen:
		fields["email"] = "длина не более 254 символов"
	case !validEmail(email):
		fields["email"] = "некорректный адрес электронной почты"
	}

	switch {
	case password == "":
		fields["password"] = "обязательное поле"
	case utf8.RuneCountInString(password) < minPasswordLen:
		fields["password"] = "не менее 8 символов"
	case len(password) > maxPasswordLen:
		fields["password"] = "не более 72 байт в UTF-8"
	}

	// Непустота проверяется по тексту без внешних пробелов; сохраняется исходное значение.
	switch {
	case strings.TrimSpace(name) == "":
		fields["name"] = "обязательное поле"
	case utf8.RuneCountInString(name) > maxNameLen:
		fields["name"] = "не более 120 символов"
	}

	if len(fields) > 0 {
		return &platform.ValidationError{Fields: fields}
	}
	return nil
}

// validEmail принимает только простой адрес local@domain без имени и угловых скобок.
func validEmail(s string) bool {
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s || addr.Name != "" {
		return false
	}
	at := strings.LastIndexByte(s, '@')
	domain := s[at+1:]
	return at > 0 && strings.Contains(domain, ".") && !strings.HasPrefix(domain, ".") && !strings.HasSuffix(domain, ".")
}
