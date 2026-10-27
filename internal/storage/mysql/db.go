// Package mysql открывает пул соединений и применяет миграции goose.
package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/pressly/goose/v3"

	"github.com/secunda/team-tasks-service/internal/config"
	"github.com/secunda/team-tasks-service/migrations"
)

// DSN собирает строку подключения: UTC, utf8mb4, тайм-ауты соединения и ожидание блокировок.
func DSN(c config.Config) string {
	mc := mysql.NewConfig()
	mc.User = c.MySQLUser
	mc.Passwd = c.MySQLPassword
	mc.Net = "tcp"
	mc.Addr = net.JoinHostPort(c.MySQLHost, strconv.Itoa(c.MySQLPort))
	mc.DBName = c.MySQLDatabase
	mc.ParseTime = true
	mc.Loc = time.UTC
	mc.Collation = "utf8mb4_0900_ai_ci"
	mc.Timeout = 5 * time.Second
	mc.ReadTimeout = c.RequestTimeout + 2*time.Second
	mc.WriteTimeout = c.RequestTimeout + 2*time.Second
	mc.Params = map[string]string{
		"time_zone":                "'+00:00'",
		"innodb_lock_wait_timeout": strconv.Itoa(int(c.MySQLLockWait.Seconds())),
	}
	return mc.FormatDSN()
}

// Open создаёт пул и проверяет доступность базы в пределах ctx.
func Open(ctx context.Context, c config.Config) (*sql.DB, error) {
	db, err := sql.Open("mysql", DSN(c))
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(time.Minute)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping mysql: %w", err)
	}
	return db, nil
}

func provider(db *sql.DB) (*goose.Provider, error) {
	return goose.NewProvider(goose.DialectMySQL, db, migrations.FS)
}

// Up применяет все ещё не применённые миграции; повторный вызов безопасен.
func Up(ctx context.Context, db *sql.DB) error {
	p, err := provider(db)
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}

// DownAll откатывает все миграции. Операция удаляет данные.
func DownAll(ctx context.Context, db *sql.DB) error {
	p, err := provider(db)
	if err != nil {
		return err
	}
	_, err = p.DownTo(ctx, 0)
	return err
}

// Version возвращает текущую применённую версию схемы (0 при пустой базе).
func Version(ctx context.Context, db *sql.DB) (int64, error) {
	p, err := provider(db)
	if err != nil {
		return 0, err
	}
	return p.GetDBVersion(ctx)
}

// LatestVersion возвращает максимальную версию встроенных миграций.
func LatestVersion() (int64, error) {
	return latestEmbedded()
}
