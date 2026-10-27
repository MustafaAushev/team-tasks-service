// Package testutil даёт тестам настоящие MySQL и Redis с изолированными данными.
//
// Зависимости задаются окружением; значения по умолчанию — в Makefile. Если запрошенный набор не может
// их получить, тест завершается ошибкой, а не пропускается.
package testutil

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/secunda/team-tasks-service/internal/config"
	"github.com/secunda/team-tasks-service/internal/platform"
	mysqlstore "github.com/secunda/team-tasks-service/internal/storage/mysql"
)

const (
	envAdminDSN  = "TEST_MYSQL_ADMIN_DSN" // например root:pw@tcp(127.0.0.1:13306)/
	envMySQLHost = "TEST_MYSQL_HOST"
	envMySQLPort = "TEST_MYSQL_PORT"
	envMySQLUser = "TEST_MYSQL_USER"
	envMySQLPass = "TEST_MYSQL_PASSWORD"
	envRedisAddr = "TEST_REDIS_ADDR"
	// envRedisEvictAddr — отдельный Redis с маленьким maxmemory и allkeys-lfu для теста вытеснения.
	envRedisEvictAddr = "TEST_REDIS_EVICT_ADDR"
)

func mustEnv(tb testing.TB, key string) string {
	tb.Helper()
	v := os.Getenv(key)
	if v == "" {
		tb.Fatalf("тестовая зависимость не настроена: переменная %s не задана (запускайте через make, см. Makefile)", key)
	}
	return v
}

// TestConfig возвращает валидную конфигурацию для тестов с явными, не дефолтными лимитами.
func TestConfig(tb testing.TB, database string) config.Config {
	tb.Helper()
	port, err := strconv.Atoi(mustEnv(tb, envMySQLPort))
	if err != nil {
		tb.Fatalf("%s: %v", envMySQLPort, err)
	}
	return config.Config{
		HTTPAddr:      "127.0.0.1:0",
		MySQLHost:     mustEnv(tb, envMySQLHost),
		MySQLPort:     port,
		MySQLUser:     mustEnv(tb, envMySQLUser),
		MySQLPassword: mustEnv(tb, envMySQLPass),
		MySQLDatabase: database,
		MySQLLockWait: 5 * time.Second,
		RedisAddr:     mustEnv(tb, envRedisAddr),

		JWTSecret:    []byte("test-jwt-secret-test-jwt-secret-0123456789"),
		JWTIssuer:    "test-issuer",
		JWTAudience:  "test-audience",
		JWTTTL:       time.Hour,
		CursorSecret: []byte("test-cursor-secret-test-cursor-secret-0123"),
		BcryptCost:   4, // минимальная стоимость только для скорости тестов

		// Лимиты заданы явно и не зависят от значений по умолчанию.
		RateUserPerMinute: 100000,
		RateUserBurst:     100000,
		RateAuthPerMinute: 100000,
		RateAuthBurst:     100000,

		RequestTimeout:  10 * time.Second,
		RedisTimeout:    500 * time.Millisecond,
		ShutdownTimeout: 5 * time.Second,
		BreakerFailures: 5,
		BreakerOpen:     30 * time.Second,
	}
}

// MySQL создаёт отдельную базу с применёнными миграциями и удаляет её после теста.
func MySQL(tb testing.TB) (*sql.DB, config.Config) {
	tb.Helper()
	admin, err := sql.Open("mysql", mustEnv(tb, envAdminDSN))
	if err != nil {
		tb.Fatalf("open admin mysql: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := admin.PingContext(ctx); err != nil {
		_ = admin.Close()
		tb.Fatalf("тестовый MySQL недоступен: %v", err)
	}

	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		tb.Fatalf("rand: %v", err)
	}
	name := "t_" + hex.EncodeToString(suffix[:])
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name+" CHARACTER SET utf8mb4"); err != nil {
		_ = admin.Close()
		tb.Fatalf("create database: %v", err)
	}
	if _, err := admin.ExecContext(ctx, "GRANT ALL ON "+name+".* TO '"+mustEnv(tb, envMySQLUser)+"'@'%'"); err != nil {
		_ = admin.Close()
		tb.Fatalf("grant: %v", err)
	}

	cfg := TestConfig(tb, name)
	db, err := mysqlstore.Open(ctx, cfg)
	if err != nil {
		_, _ = admin.Exec("DROP DATABASE " + name)
		_ = admin.Close()
		tb.Fatalf("open test db: %v", err)
	}
	if err := mysqlstore.Up(ctx, db); err != nil {
		_ = db.Close()
		_, _ = admin.Exec("DROP DATABASE " + name)
		_ = admin.Close()
		tb.Fatalf("migrate test db: %v", err)
	}
	tb.Cleanup(func() {
		_ = db.Close()
		_, _ = admin.Exec("DROP DATABASE IF EXISTS " + name)
		_ = admin.Close()
	})
	return db, cfg
}

// Redis возвращает клиент настоящего Redis; ключи теста изолированы префиксом в вызывающем коде.
func Redis(tb testing.TB) *redis.Client {
	tb.Helper()
	return redisAt(tb, mustEnv(tb, envRedisAddr))
}

// RedisEvict возвращает клиент отдельного Redis с маленьким maxmemory для проверки вытеснения.
func RedisEvict(tb testing.TB) *redis.Client {
	tb.Helper()
	return redisAt(tb, mustEnv(tb, envRedisEvictAddr))
}

func redisAt(tb testing.TB, addr string) *redis.Client {
	tb.Helper()
	rc := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rc.Ping(ctx).Err(); err != nil {
		_ = rc.Close()
		tb.Fatalf("тестовый Redis недоступен: %v", err)
	}
	tb.Cleanup(func() { _ = rc.Close() })
	return rc
}

// CacheKeys возвращает ключи кеша списков команды (все ревизии).
func CacheKeys(tb testing.TB, rc *redis.Client, teamID string) []string {
	tb.Helper()
	var keys []string
	iter := rc.Scan(context.Background(), 0, "tasks:v1:"+teamID+":*", 1000).Iterator()
	for iter.Next(context.Background()) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		tb.Fatalf("scan redis: %v", err)
	}
	return keys
}

// DeleteTeamCacheKeys удаляет ключи кеша всех команд тестовой базы: тесты разделяют один Redis,
// поэтому чистят только свои команды, а не весь Redis.
func DeleteTeamCacheKeys(tb testing.TB, db *sql.DB, rc *redis.Client) {
	tb.Helper()
	rows, err := db.Query(`SELECT id FROM teams`)
	if err != nil {
		return // база уже удалена или закрыта — ключи истекут по TTL
	}
	defer rows.Close()
	for rows.Next() {
		var b []byte
		if rows.Scan(&b) != nil {
			return
		}
		id, err := platform.IDFromBytes(b)
		if err != nil {
			return
		}
		if keys := CacheKeys(tb, rc, platform.IDString(id)); len(keys) > 0 {
			_ = rc.Del(context.Background(), keys...).Err()
		}
	}
}
