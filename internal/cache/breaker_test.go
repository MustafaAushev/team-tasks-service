package cache

import (
	"testing"
	"time"

	"github.com/secunda/team-tasks-service/internal/platform"
)

type transitions []string

func (tr *transitions) record(from, to State) { *tr = append(*tr, string(from)+"->"+string(to)) }

func call(t *testing.T, b *Breaker, o Outcome) {
	t.Helper()
	done, ok := b.Allow()
	if !ok {
		t.Fatalf("обращение к Redis не пропущено в состоянии %s", b.State())
	}
	done(o)
}

func bypassed(t *testing.T, b *Breaker) {
	t.Helper()
	if _, ok := b.Allow(); ok {
		t.Fatalf("ожидался обход Redis в состоянии %s", b.State())
	}
}

// Точная граница открытия, обход во время open, одна проба, закрытие после успеха и повторное
// открытие после отказа пробы — на управляемых часах, без sleep.
func TestBreakerStates(t *testing.T) {
	clock := &platform.FixedClock{T: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	var tr transitions
	b := NewBreaker(5, 30*time.Second, clock, tr.record)

	// Четыре отказа подряд не открывают; успех (в том числе промах) обнуляет серию.
	for i := 0; i < 4; i++ {
		call(t, b, Failure)
	}
	call(t, b, Success)
	for i := 0; i < 4; i++ {
		call(t, b, Failure)
	}
	if b.State() != StateClosed {
		t.Fatalf("открыт после 4 отказов подряд: %v", tr)
	}
	// Пятый последовательный отказ открывает.
	call(t, b, Failure)
	if b.State() != StateOpen {
		t.Fatalf("не открыт на 5-м отказе: %v", tr)
	}
	bypassed(t, b)
	clock.Advance(30*time.Second - time.Nanosecond)
	bypassed(t, b)

	// Через 30 секунд одно обращение становится пробой, остальные в это время обходят Redis.
	clock.Advance(time.Nanosecond)
	probe, ok := b.Allow()
	if !ok || b.State() != StateHalfOpen {
		t.Fatalf("проба не началась: %s", b.State())
	}
	bypassed(t, b)
	// Отказ пробы снова открывает на 30 секунд.
	probe(Failure)
	if b.State() != StateOpen {
		t.Fatalf("после отказа пробы: %s", b.State())
	}
	clock.Advance(29 * time.Second)
	bypassed(t, b)
	clock.Advance(time.Second)

	// Отменённая проба не меняет состояние: следующее обращение — новая проба.
	call(t, b, Ignored)
	if b.State() != StateHalfOpen {
		t.Fatalf("отмена пробы изменила состояние: %s", b.State())
	}
	// Успешная проба закрывает.
	call(t, b, Success)
	if b.State() != StateClosed {
		t.Fatalf("успешная проба не закрыла: %s", b.State())
	}

	want := []string{"closed->open", "open->half_open", "half_open->open", "open->half_open", "half_open->closed"}
	if len(tr) != len(want) {
		t.Fatalf("переходы %v, ожидались %v", tr, want)
	}
	for i := range want {
		if tr[i] != want[i] {
			t.Fatalf("переходы %v, ожидались %v", tr, want)
		}
	}
}

// Отмена клиента не считается ни отказом, ни успехом: серия отказов не прерывается и не растёт.
func TestBreakerIgnoresCancellation(t *testing.T) {
	clock := &platform.FixedClock{T: time.Unix(0, 0)}
	b := NewBreaker(5, 30*time.Second, clock, nil)
	for i := 0; i < 4; i++ {
		call(t, b, Failure)
		call(t, b, Ignored)
	}
	if b.State() != StateClosed {
		t.Fatal("отмена засчитана как отказ")
	}
	call(t, b, Failure)
	if b.State() != StateOpen {
		t.Fatal("отмена прервала серию отказов")
	}
}

// Итоги обращений, начатых до перехода, не влияют на новое состояние.
func TestBreakerIgnoresStaleOutcomes(t *testing.T) {
	clock := &platform.FixedClock{T: time.Unix(0, 0)}
	b := NewBreaker(2, time.Minute, clock, nil)
	slow, _ := b.Allow() // начато в closed
	call(t, b, Failure)
	call(t, b, Failure) // открыт
	slow(Success)       // запоздалый успех не закрывает breaker
	if b.State() != StateOpen {
		t.Fatalf("запоздалый итог изменил состояние: %s", b.State())
	}
	clock.Advance(time.Minute)
	probe, _ := b.Allow()
	late, ok := b.Allow()
	if ok || late != nil {
		t.Fatal("вторая проба пропущена")
	}
	probe(Success)
	if b.State() != StateClosed {
		t.Fatal("проба не закрыла")
	}
}
