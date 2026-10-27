// Package auth отвечает за регистрацию, вход и выдачу/проверку JWT. Командные роли здесь
// не определяются: они всегда читаются из MySQL по текущему членству.
package auth

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
)

// ErrInvalidToken — любой дефект токена; причина клиенту не раскрывается.
var ErrInvalidToken = errors.New("invalid token")

// Единственный разрешённый алгоритм подписи.
const signingAlg = "HS256"

// TokenManager выдаёт и проверяет access token.
type TokenManager struct {
	secret   []byte
	issuer   string
	audience string
	ttl      time.Duration
	clock    platform.Clock
}

func NewTokenManager(secret []byte, issuer, audience string, ttl time.Duration, clock platform.Clock) *TokenManager {
	return &TokenManager{secret: secret, issuer: issuer, audience: audience, ttl: ttl, clock: clock}
}

// Issue возвращает подписанный токен и срок жизни в секундах.
// sub содержит UUIDv7 пользователя строкой; ролей команды в токене нет.
func (m *TokenManager) Issue(userID uuid.UUID) (token string, expiresIn int64, err error) {
	now := m.clock.Now()
	claims := jwt.RegisteredClaims{
		Subject:   platform.IDString(userID),
		Issuer:    m.issuer,
		Audience:  jwt.ClaimStrings{m.audience},
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(m.ttl)),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(m.secret)
	if err != nil {
		return "", 0, fmt.Errorf("sign token: %w", err)
	}
	return signed, int64(m.ttl.Seconds()), nil
}

// Verify проверяет подпись, алгоритм, срок, issuer, audience и формат sub.
func (m *TokenManager) Verify(raw string) (uuid.UUID, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(raw, &claims,
		func(*jwt.Token) (any, error) { return m.secret, nil },
		jwt.WithValidMethods([]string{signingAlg}),
		jwt.WithIssuer(m.issuer),
		jwt.WithAudience(m.audience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithTimeFunc(m.clock.Now),
	)
	if err != nil {
		return uuid.Nil, ErrInvalidToken
	}
	id, err := platform.ParseID(claims.Subject)
	if err != nil {
		return uuid.Nil, ErrInvalidToken
	}
	return id, nil
}
