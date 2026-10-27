package ratelimit

import (
	"testing"
	"time"

	"github.com/secunda/team-tasks-service/internal/platform"
)

// Значения по умолчанию: 120 в минуту с burst 20 — 20 запросов подряд, затем токен каждые 0,5 с.
func TestRateLimiter(t *testing.T) {
	clock := &platform.FixedClock{T: time.Unix(0, 0)}
	l := New(120, 20, clock)
	for i := 0; i < 20; i++ {
		if ok, _ := l.Allow("u1"); !ok {
			t.Fatalf("запрос %d в пределах burst отклонён", i+1)
		}
	}
	ok, retry := l.Allow("u1")
	if ok || retry != 500*time.Millisecond {
		t.Fatalf("21-й запрос: ok=%v retry=%v", ok, retry)
	}
	// Другой ключ не затронут.
	if ok, _ := l.Allow("u2"); !ok {
		t.Fatal("лимит одного ключа ограничил другой")
	}
	clock.Advance(499 * time.Millisecond)
	if ok, _ := l.Allow("u1"); ok {
		t.Fatal("токен появился раньше срока")
	}
	clock.Advance(time.Millisecond)
	if ok, _ := l.Allow("u1"); !ok {
		t.Fatal("токен не восстановился через 0,5 с")
	}
	// Накопление не превышает burst.
	clock.Advance(time.Hour)
	for i := 0; i < 20; i++ {
		l.Allow("u1")
	}
	if ok, _ := l.Allow("u1"); ok {
		t.Fatal("накоплено больше burst")
	}
}

// Неактивные ключи удаляются: их bucket к этому моменту полон, поэтому поведение не меняется.
func TestRateLimiterCleanup(t *testing.T) {
	clock := &platform.FixedClock{T: time.Unix(0, 0)}
	l := New(60, 20, clock) // полное восстановление за 20 с
	for i := 0; i < 100; i++ {
		l.Allow(string(rune('a'+i%26)) + string(rune('A'+i/26)))
	}
	if l.Len() != 100 {
		t.Fatalf("ключей: %d", l.Len())
	}
	clock.Advance(10 * time.Second)
	l.Allow("active")
	if l.Len() != 101 {
		t.Fatalf("очистка раньше срока: %d", l.Len())
	}
	clock.Advance(10 * time.Second)
	l.Allow("active")
	if l.Len() != 1 {
		t.Fatalf("после 20 с бездействия остались ключи: %d", l.Len())
	}
}
