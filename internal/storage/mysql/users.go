package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"

	"github.com/secunda/team-tasks-service/internal/auth"
	"github.com/secunda/team-tasks-service/internal/platform"
)

// errDupEntry — код MySQL ER_DUP_ENTRY.
const errDupEntry = 1062

// UserStore реализует auth.Store поверх database/sql.
type UserStore struct{ DB *sql.DB }

var _ auth.Store = UserStore{}

func (s UserStore) CreateUser(ctx context.Context, u auth.User) error {
	_, err := s.DB.ExecContext(ctx,
		`INSERT INTO users (id, email, password_hash, name, created_at) VALUES (?, ?, ?, ?, ?)`,
		platform.IDBytes(u.ID), u.Email, u.PasswordHash, u.Name, u.CreatedAt.UTC())
	if err != nil {
		var me *mysql.MySQLError
		if errors.As(err, &me) && me.Number == errDupEntry {
			return auth.ErrEmailExists
		}
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

func (s UserStore) FindByEmail(ctx context.Context, email string) (auth.User, error) {
	var (
		u   auth.User
		raw []byte
	)
	err := s.DB.QueryRowContext(ctx,
		`SELECT id, email, password_hash, name, created_at FROM users WHERE email = ?`, email).
		Scan(&raw, &u.Email, &u.PasswordHash, &u.Name, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.User{}, auth.ErrUserNotFound
	}
	if err != nil {
		return auth.User{}, fmt.Errorf("select user by email: %w", err)
	}
	if u.ID, err = platform.IDFromBytes(raw); err != nil {
		return auth.User{}, err
	}
	u.CreatedAt = u.CreatedAt.UTC()
	return u, nil
}
