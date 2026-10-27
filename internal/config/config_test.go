package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/secunda/team-tasks-service/internal/config"
)

func validEnv() map[string]string {
	return map[string]string{
		"MYSQL_HOST":     "db",
		"MYSQL_USER":     "u",
		"MYSQL_PASSWORD": "p",
		"MYSQL_DATABASE": "d",
		"REDIS_HOST":     "redis",
		"JWT_SECRET":     strings.Repeat("a", 32),
		"CURSOR_SECRET":  strings.Repeat("b", 32),
	}
}

func load(env map[string]string) (config.Config, error) {
	return config.Load(func(k string) string { return env[k] })
}

func TestLoadDefaults(t *testing.T) {
	c, err := load(validEnv())
	if err != nil {
		t.Fatal(err)
	}
	if c.RateUserPerMinute != 120 || c.RateUserBurst != 20 || c.RateAuthPerMinute != 60 || c.RateAuthBurst != 20 {
		t.Errorf("лимиты по умолчанию: %+v", c)
	}
	if c.JWTTTL != time.Hour || c.BcryptCost != 12 || c.RequestTimeout != 10*time.Second ||
		c.RedisTimeout != 100*time.Millisecond || c.MySQLLockWait != 5*time.Second ||
		c.ShutdownTimeout != 10*time.Second || c.BreakerFailures != 5 || c.BreakerOpen != 30*time.Second {
		t.Errorf("значения по умолчанию: %+v", c)
	}
	if c.TrustProxyHeaders {
		t.Error("доверие заголовкам прокси должно быть выключено по умолчанию")
	}
	if c.RedisAddr != "redis:6379" {
		t.Errorf("адрес Redis по умолчанию: %q", c.RedisAddr)
	}
}

func TestLoadRejectsMissingSecrets(t *testing.T) {
	env := validEnv()
	delete(env, "JWT_SECRET")
	delete(env, "MYSQL_PASSWORD")
	_, err := load(env)
	if err == nil {
		t.Fatal("ожидался отказ без обязательных секретов")
	}
	msg := err.Error()
	for _, want := range []string{"JWT_SECRET", "MYSQL_PASSWORD"} {
		if !strings.Contains(msg, want) {
			t.Errorf("в ошибке нет %s: %s", want, msg)
		}
	}
}

func TestLoadRejectsShortSecretWithoutLeakingIt(t *testing.T) {
	env := validEnv()
	env["CURSOR_SECRET"] = "short-secret-value"
	_, err := load(env)
	if err == nil || !strings.Contains(err.Error(), "CURSOR_SECRET") {
		t.Fatalf("ожидалась ошибка короткого секрета: %v", err)
	}
	if strings.Contains(err.Error(), "short-secret-value") {
		t.Fatal("значение секрета попало в ошибку")
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	cases := map[string]string{
		"BCRYPT_COST":          "2",
		"MYSQL_PORT":           "abc",
		"REDIS_PORT":           "70000",
		"REDIS_HOST":           "",
		"RATE_USER_PER_MINUTE": "0",
		"RATE_AUTH_BURST":      "0",
		"JWT_TTL":              "-1h",
		"REQUEST_TIMEOUT":      "soon",
		"TRUST_PROXY_HEADERS":  "maybe",
	}
	for key, val := range cases {
		t.Run(key, func(t *testing.T) {
			env := validEnv()
			env[key] = val
			_, err := load(env)
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("ожидалась ошибка по %s: %v", key, err)
			}
		})
	}
}

func TestLoadOverrides(t *testing.T) {
	env := validEnv()
	env["RATE_USER_PER_MINUTE"] = "5"
	env["RATE_USER_BURST"] = "2"
	env["JWT_TTL"] = "15m"
	env["BCRYPT_COST"] = "10"
	env["TRUST_PROXY_HEADERS"] = "true"
	env["REDIS_PORT"] = "6380"
	c, err := load(env)
	if err != nil {
		t.Fatal(err)
	}
	if c.RateUserPerMinute != 5 || c.RateUserBurst != 2 || c.JWTTTL != 15*time.Minute ||
		c.BcryptCost != 10 || !c.TrustProxyHeaders || c.RedisAddr != "redis:6380" {
		t.Errorf("переопределения не применены: %+v", c)
	}
}

// migrate читает только MySQL: Redis и секреты ему не нужны и не могут сломать миграции.
func TestLoadMySQLIgnoresOtherSettings(t *testing.T) {
	env := map[string]string{
		"MYSQL_HOST": "db", "MYSQL_USER": "u", "MYSQL_PASSWORD": "p", "MYSQL_DATABASE": "d",
		"REDIS_PORT": "not-a-port", "JWT_SECRET": "short",
	}
	c, err := config.LoadMySQL(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if c.MySQLHost != "db" || c.MySQLPort != 3306 || c.MySQLLockWait != 5*time.Second || c.RequestTimeout != 10*time.Second {
		t.Errorf("настройки MySQL: %+v", c)
	}

	env["MYSQL_PORT"], env["REQUEST_TIMEOUT"] = "0", "0s"
	delete(env, "MYSQL_DATABASE")
	_, err = config.LoadMySQL(func(k string) string { return env[k] })
	for _, want := range []string{"MYSQL_PORT", "REQUEST_TIMEOUT", "MYSQL_DATABASE"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("нет ошибки %s: %v", want, err)
		}
	}
}
