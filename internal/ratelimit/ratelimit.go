// Package ratelimit — локальный token bucket в памяти процесса. Он не зависит от Redis: отказ кеша
// или открытый circuit breaker не снимают ограничение нагрузки. У каждого процесса свои лимиты,
// после перезапуска состояние сбрасывается.
package ratelimit

import (
	"math"
	"sync"
	"time"

	"github.com/secunda/team-tasks-service/internal/platform"
)

// Limiter ограничивает частоту по ключу (user_id, IP): perMinute токенов в минуту, не больше burst
// подряд. Безопасен для конкурентного использования.
type Limiter struct {
	perSecond float64
	burst     float64
	clock     platform.Clock
	// idle — за это время пустой bucket наполняется полностью: такой bucket неотличим от нового и
	// удаляется при очистке без изменения поведения.
	idle time.Duration

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func New(perMinute float64, burst int, clock platform.Clock) *Limiter {
	perSecond := perMinute / 60
	return &Limiter{
		perSecond: perSecond,
		burst:     float64(burst),
		clock:     clock,
		idle:      time.Duration(float64(burst) / perSecond * float64(time.Second)),
		buckets:   map[string]*bucket{},
		lastSweep: clock.Now(),
	}
}

// Allow расходует токен ключа. false — лимит исчерпан; retryAfter — когда появится следующий токен.
func (l *Limiter) Allow(key string) (ok bool, retryAfter time.Duration) {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)

	b, found := l.buckets[key]
	if !found {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	if elapsed := now.Sub(b.last).Seconds(); elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+elapsed*l.perSecond)
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.perSecond * float64(time.Second))
}

// Len — число отслеживаемых ключей (для проверки очистки).
func (l *Limiter) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.buckets)
}

// sweep раз в idle удаляет ключи, неактивные дольше idle: их bucket уже полон.
func (l *Limiter) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < l.idle {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if now.Sub(b.last) >= l.idle {
			delete(l.buckets, k)
		}
	}
}
