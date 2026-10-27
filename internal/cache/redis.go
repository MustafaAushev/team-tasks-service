// Package cache содержит клиент Redis и адаптер кеша списков задач.
package cache

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/secunda/team-tasks-service/internal/config"
)

// NewClient создаёт клиент Redis с тайм-аутами операций из конфигурации.
// Соединение не проверяется: Redis не является условием запуска и готовности.
func NewClient(c config.Config) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:         c.RedisAddr,
		Password:     c.RedisPassword,
		DialTimeout:  c.RedisTimeout,
		ReadTimeout:  c.RedisTimeout,
		WriteTimeout: c.RedisTimeout,
		PoolTimeout:  c.RedisTimeout,
		MaxRetries:   0,
	})
}

// Ping проверяет доступность Redis (используется в тестах и диагностике).
func Ping(ctx context.Context, rc *redis.Client) error {
	if err := rc.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("ping redis: %w", err)
	}
	return nil
}
