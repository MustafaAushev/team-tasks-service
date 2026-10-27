package httpapi

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/ratelimit"
)

// RateLimits — локальные лимиты процесса. Нулевая скорость отключает соответствующий лимит
// (используется в unit-тестах транспорта, которым лимиты не нужны).
type RateLimits struct {
	UserPerMinute float64
	UserBurst     int
	AuthPerMinute float64
	AuthBurst     int
	// TrustProxyHeaders — брать IP клиента из X-Forwarded-For/X-Real-IP (только за доверенным прокси).
	TrustProxyHeaders bool
}

const codeRateLimited = "rate_limit_exceeded"

// limit отклоняет запрос с 429 и Retry-After до бизнес-операции, если ключ исчерпал лимит.
func limit(l *ratelimit.Limiter, key func(*http.Request) string, next http.Handler) http.Handler {
	if l == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ok, retry := l.Allow(key(r))
		if !ok {
			e := newError(http.StatusTooManyRequests, codeRateLimited, "Слишком много запросов, повторите позже")
			e.Headers = map[string]string{"Retry-After": strconv.Itoa(int(math.Max(1, math.Ceil(retry.Seconds()))))}
			writeError(w, r, e)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func newLimiter(perMinute float64, burst int, clock platform.Clock) *ratelimit.Limiter {
	if perMinute <= 0 || burst <= 0 {
		return nil
	}
	if clock == nil {
		clock = platform.SystemClock{}
	}
	return ratelimit.New(perMinute, burst, clock)
}

// userKey — лимит защищённых операций по пользователю из JWT.
func userKey(r *http.Request) string { return "user:" + platform.IDString(identity(r)) }

// ipKey — общий лимит регистрации и входа по IP. По умолчанию IP берётся только из RemoteAddr.
func ipKey(trustProxy bool) func(*http.Request) string {
	return func(r *http.Request) string {
		if trustProxy {
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				first, _, _ := strings.Cut(xff, ",")
				if ip := net.ParseIP(strings.TrimSpace(first)); ip != nil {
					return "ip:" + ip.String()
				}
			}
			if ip := net.ParseIP(strings.TrimSpace(r.Header.Get("X-Real-IP"))); ip != nil {
				return "ip:" + ip.String()
			}
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		return "ip:" + host
	}
}
