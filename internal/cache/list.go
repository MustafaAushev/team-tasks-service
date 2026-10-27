package cache

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/secunda/team-tasks-service/internal/config"
	"github.com/secunda/team-tasks-service/internal/platform"
)

// ListTTL — срок жизни кешированного списка задач; задаётся при записи и не продлевается при чтении.
const ListTTL = 300 * time.Second

// ListCache — адаптер Redis для кеша списков задач. Любой сбой Redis или открытый breaker дают
// промах, и список читается из MySQL: ошибка кеша никогда не становится ошибкой запроса.
type ListCache struct {
	rc      redis.Cmdable
	breaker *Breaker
	timeout time.Duration
	logger  *slog.Logger
}

// NewListCache связывает клиент Redis с breaker из конфигурации; переходы breaker логируются.
func NewListCache(rc redis.Cmdable, cfg config.Config, clock platform.Clock, logger *slog.Logger) *ListCache {
	c := &ListCache{rc: rc, timeout: cfg.RedisTimeout, logger: logger}
	c.breaker = NewBreaker(cfg.BreakerFailures, cfg.BreakerOpen, clock, func(from, to State) {
		level := slog.LevelInfo
		if to == StateOpen {
			level = slog.LevelError
		}
		meta := map[string]any{"event": "breaker_" + string(to), "from": string(from), "dependency": "redis"}
		if to == StateOpen {
			// В open Redis обходится, списки читаются из MySQL до первой пробы.
			meta["bypass_for_ms"] = cfg.BreakerOpen.Milliseconds()
		}
		logger.Log(context.Background(), level, "circuit breaker: "+string(from)+" -> "+string(to),
			"context", "redis", "request_id", nil, "meta", meta)
	})
	return c
}

// Breaker возвращает breaker адаптера (для диагностики и тестов).
func (c *ListCache) Breaker() *Breaker { return c.breaker }

// Get возвращает значение ключа; false — промах, сбой Redis или открытый breaker.
func (c *ListCache) Get(ctx context.Context, key string) ([]byte, bool) {
	var val []byte
	err := c.do(ctx, "get", func(ctx context.Context) error {
		var err error
		val, err = c.rc.Get(ctx, key).Bytes()
		return err
	})
	return val, err == nil
}

// Set записывает значение с ListTTL. Сбой записи только логируется.
func (c *ListCache) Set(ctx context.Context, key string, value []byte) {
	_ = c.do(ctx, "set", func(ctx context.Context) error {
		return c.rc.Set(ctx, key, value, ListTTL).Err()
	})
}

// errBypassed — Redis не вызывался: breaker открыт или занят пробой.
var errBypassed = errors.New("redis bypassed by circuit breaker")

// do выполняет операцию через breaker с бюджетом REDIS_TIMEOUT и классифицирует итог:
// redis.Nil — обычный промах и успешное обращение; отмена запроса клиентом не учитывается;
// всё остальное — технический отказ.
func (c *ListCache) do(ctx context.Context, op string, fn func(context.Context) error) error {
	done, ok := c.breaker.Allow()
	if !ok {
		return errBypassed
	}
	opCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	err := fn(opCtx)
	switch {
	case err == nil || errors.Is(err, redis.Nil):
		done(Success)
	case ctx.Err() != nil:
		done(Ignored)
	default:
		done(Failure)
		c.logger.ErrorContext(ctx, "redis "+op+" failed: "+err.Error(),
			"context", "redis", "request_id", platform.RequestID(ctx),
			"meta", map[string]any{"event": "cache_error", "operation": op, "dependency": "redis"})
	}
	return err
}
