package platform

import (
	"sync"
	"time"
)

// Clock изолирует источник времени, чтобы JWT, метрики и лимитеры управлялись в тестах.
type Clock interface {
	Now() time.Time
}

// SystemClock возвращает серверное время в UTC.
type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// FixedClock возвращает управляемое время; используется в тестах. Set и Advance безопасны для
// конкурентного использования (сервер читает время в своих горутинах).
type FixedClock struct {
	T  time.Time
	mu sync.Mutex
}

func (c *FixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.T.UTC()
}

// Set устанавливает время.
func (c *FixedClock) Set(t time.Time) {
	c.mu.Lock()
	c.T = t
	c.mu.Unlock()
}

// Advance сдвигает время вперёд.
func (c *FixedClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.T = c.T.Add(d)
	c.mu.Unlock()
}
