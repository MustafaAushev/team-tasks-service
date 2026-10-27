// Package config загружает и проверяет конфигурацию из переменных окружения.
package config

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const minSecretBytes = 32

// Config содержит все настройки процесса. Секреты не имеют значений по умолчанию.
type Config struct {
	HTTPAddr string

	MySQLHost     string
	MySQLPort     int
	MySQLUser     string
	MySQLPassword string
	MySQLDatabase string
	// MySQLLockWait задаёт ожидание SQL-блокировки (innodb_lock_wait_timeout).
	MySQLLockWait time.Duration

	// RedisAddr собирается из REDIS_HOST и REDIS_PORT.
	RedisAddr     string
	RedisPassword string

	JWTSecret   []byte
	JWTIssuer   string
	JWTAudience string
	JWTTTL      time.Duration

	CursorSecret []byte
	BcryptCost   int

	RateUserPerMinute float64
	RateUserBurst     int
	RateAuthPerMinute float64
	RateAuthBurst     int

	RequestTimeout  time.Duration
	RedisTimeout    time.Duration
	ShutdownTimeout time.Duration

	BreakerFailures int
	BreakerOpen     time.Duration

	TrustProxyHeaders bool
}

// LoadMySQL читает только настройки подключения к MySQL (и REQUEST_TIMEOUT, от которого зависят
// тайм-ауты соединения). Нужна командам, которым не нужны Redis и секреты, например migrate.
func LoadMySQL(getenv func(string) string) (Config, error) {
	l := loader{getenv: getenv}
	var c Config
	l.mysql(&c)
	if err := l.err(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Load читает конфигурацию через getenv (обычно os.Getenv) и возвращает все найденные ошибки.
func Load(getenv func(string) string) (Config, error) {
	l := loader{getenv: getenv}

	c := Config{
		HTTPAddr:      l.str("HTTP_ADDR", ":8080"),
		RedisPassword: l.str("REDIS_PASSWORD", ""),

		JWTSecret:   l.secret("JWT_SECRET"),
		JWTIssuer:   l.str("JWT_ISSUER", "team-tasks-service"),
		JWTAudience: l.str("JWT_AUDIENCE", "team-tasks-api"),
		JWTTTL:      l.duration("JWT_TTL", time.Hour),

		CursorSecret: l.secret("CURSOR_SECRET"),
		BcryptCost:   l.integer("BCRYPT_COST", 12),

		RateUserPerMinute: l.float("RATE_USER_PER_MINUTE", 120),
		RateUserBurst:     l.integer("RATE_USER_BURST", 20),
		RateAuthPerMinute: l.float("RATE_AUTH_PER_MINUTE", 60),
		RateAuthBurst:     l.integer("RATE_AUTH_BURST", 20),

		RedisTimeout:    l.duration("REDIS_TIMEOUT", 100*time.Millisecond),
		ShutdownTimeout: l.duration("SHUTDOWN_TIMEOUT", 10*time.Second),

		BreakerFailures: l.integer("BREAKER_FAILURES", 5),
		BreakerOpen:     l.duration("BREAKER_OPEN", 30*time.Second),

		TrustProxyHeaders: l.boolean("TRUST_PROXY_HEADERS", false),
	}

	l.mysql(&c)
	if c.BcryptCost < 4 || c.BcryptCost > 31 {
		l.errs = append(l.errs, fmt.Errorf("BCRYPT_COST: %d вне диапазона 4..31", c.BcryptCost))
	}
	redisHost, redisPort := l.required("REDIS_HOST"), l.integer("REDIS_PORT", 6379)
	c.RedisAddr = net.JoinHostPort(redisHost, strconv.Itoa(redisPort))
	l.port("REDIS_PORT", redisPort)
	for name, v := range map[string]float64{
		"RATE_USER_PER_MINUTE": c.RateUserPerMinute,
		"RATE_AUTH_PER_MINUTE": c.RateAuthPerMinute,
	} {
		if v <= 0 {
			l.errs = append(l.errs, fmt.Errorf("%s: должно быть больше 0", name))
		}
	}
	for name, v := range map[string]int{
		"RATE_USER_BURST":  c.RateUserBurst,
		"RATE_AUTH_BURST":  c.RateAuthBurst,
		"BREAKER_FAILURES": c.BreakerFailures,
	} {
		if v < 1 {
			l.errs = append(l.errs, fmt.Errorf("%s: должно быть не меньше 1", name))
		}
	}
	for name, v := range map[string]time.Duration{
		"JWT_TTL":          c.JWTTTL,
		"REDIS_TIMEOUT":    c.RedisTimeout,
		"SHUTDOWN_TIMEOUT": c.ShutdownTimeout,
		"BREAKER_OPEN":     c.BreakerOpen,
	} {
		if v <= 0 {
			l.errs = append(l.errs, fmt.Errorf("%s: должно быть больше 0", name))
		}
	}

	if err := l.err(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// mysql заполняет настройки MySQL и REQUEST_TIMEOUT и проверяет их; общая часть Load и LoadMySQL.
func (l *loader) mysql(c *Config) {
	c.MySQLHost = l.required("MYSQL_HOST")
	c.MySQLPort = l.integer("MYSQL_PORT", 3306)
	c.MySQLUser = l.required("MYSQL_USER")
	c.MySQLPassword = l.required("MYSQL_PASSWORD")
	c.MySQLDatabase = l.required("MYSQL_DATABASE")
	c.MySQLLockWait = l.duration("MYSQL_LOCK_WAIT", 5*time.Second)
	c.RequestTimeout = l.duration("REQUEST_TIMEOUT", 10*time.Second)

	l.port("MYSQL_PORT", c.MySQLPort)
	for name, v := range map[string]time.Duration{
		"MYSQL_LOCK_WAIT": c.MySQLLockWait,
		"REQUEST_TIMEOUT": c.RequestTimeout,
	} {
		if v <= 0 {
			l.errs = append(l.errs, fmt.Errorf("%s: должно быть больше 0", name))
		}
	}
}

func (l *loader) port(key string, v int) {
	if v < 1 || v > 65535 {
		l.errs = append(l.errs, fmt.Errorf("%s: %d вне диапазона 1..65535", key, v))
	}
}

type loader struct {
	getenv func(string) string
	errs   []error
}

func (l *loader) err() error {
	if len(l.errs) == 0 {
		return nil
	}
	return fmt.Errorf("некорректная конфигурация: %w", errors.Join(l.errs...))
}

func (l *loader) str(key, def string) string {
	if v := strings.TrimSpace(l.getenv(key)); v != "" {
		return v
	}
	return def
}

func (l *loader) required(key string) string {
	v := strings.TrimSpace(l.getenv(key))
	if v == "" {
		l.errs = append(l.errs, fmt.Errorf("%s: обязательная переменная не задана", key))
	}
	return v
}

// secret возвращает секрет длиной не менее 32 байт; значение секрета в ошибки не попадает.
func (l *loader) secret(key string) []byte {
	v := l.getenv(key)
	if v == "" {
		l.errs = append(l.errs, fmt.Errorf("%s: обязательный секрет не задан", key))
		return nil
	}
	if len(v) < minSecretBytes {
		l.errs = append(l.errs, fmt.Errorf("%s: секрет короче %d байт", key, minSecretBytes))
		return nil
	}
	return []byte(v)
}

func (l *loader) integer(key string, def int) int {
	raw := strings.TrimSpace(l.getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: ожидалось целое число", key))
		return def
	}
	return v
}

func (l *loader) float(key string, def float64) float64 {
	raw := strings.TrimSpace(l.getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: ожидалось число", key))
		return def
	}
	return v
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(l.getenv(key))
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: ожидалась длительность вида 5s или 100ms", key))
		return def
	}
	return v
}

func (l *loader) boolean(key string, def bool) bool {
	raw := strings.TrimSpace(l.getenv(key))
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		l.errs = append(l.errs, fmt.Errorf("%s: ожидалось true или false", key))
		return def
	}
	return v
}
