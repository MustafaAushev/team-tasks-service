package cache

import (
	"sync"
	"time"

	"github.com/secunda/team-tasks-service/internal/platform"
)

// State — состояние circuit breaker.
type State string

const (
	StateClosed   State = "closed"
	StateOpen     State = "open"
	StateHalfOpen State = "half_open"
)

// Outcome — итог обращения к Redis для breaker.
type Outcome int

const (
	// Success — Redis ответил, в том числе промахом (redis.Nil): сбрасывает серию отказов.
	Success Outcome = iota
	// Failure — технический отказ Redis: увеличивает серию отказов.
	Failure
	// Ignored — обращение прервано отменой клиента: не меняет ни серию отказов, ни состояние.
	Ignored
)

// Breaker — circuit breaker перед Redis. Открывается на failures-м последовательном отказе и
// пропускает Redis open-интервал; затем одно обращение становится пробой (half-open), остальные
// в это время обходят Redis. Успешная проба закрывает breaker, отказ снова открывает его.
// Фонового опроса нет: проба выполняется только обращением приложения.
//
// Своя реализация вместо sony/gobreaker: отмена клиента не должна считаться ни отказом, ни
// успехом, а границы времени проверяются в тестах управляемыми часами.
type Breaker struct {
	failures int
	open     time.Duration
	clock    platform.Clock
	onChange func(from, to State)

	mu          sync.Mutex
	state       State
	consecutive int
	openUntil   time.Time
	probing     bool
	generation  uint64 // меняется при каждом переходе: итоги обращений прошлого состояния не учитываются
}

// NewBreaker создаёт закрытый breaker; onChange (может быть nil) вызывается при каждом переходе.
func NewBreaker(failures int, open time.Duration, clock platform.Clock, onChange func(from, to State)) *Breaker {
	return &Breaker{failures: failures, open: open, clock: clock, onChange: onChange, state: StateClosed}
}

// State возвращает текущее состояние (open с истёкшим интервалом остаётся open до следующего обращения).
func (b *Breaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// Allow решает, обращаться ли к Redis. false — Redis нужно обойти. При true вызывающий обязан
// сообщить итог через done ровно один раз.
func (b *Breaker) Allow() (done func(Outcome), ok bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case StateOpen:
		if b.clock.Now().Before(b.openUntil) {
			return nil, false
		}
		b.transition(StateHalfOpen)
		fallthrough
	case StateHalfOpen:
		if b.probing {
			return nil, false
		}
		b.probing = true
	}
	gen := b.generation
	return func(o Outcome) { b.report(gen, o) }, true
}

func (b *Breaker) report(gen uint64, o Outcome) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if gen != b.generation {
		return
	}
	switch b.state {
	case StateClosed:
		switch o {
		case Success:
			b.consecutive = 0
		case Failure:
			b.consecutive++
			if b.consecutive >= b.failures {
				b.trip()
			}
		}
	case StateHalfOpen:
		b.probing = false
		switch o {
		case Success:
			b.consecutive = 0
			b.transition(StateClosed)
		case Failure:
			b.trip()
		}
		// Ignored: проба отменена клиентом — следующее обращение станет новой пробой.
	}
}

func (b *Breaker) trip() {
	b.openUntil = b.clock.Now().Add(b.open)
	b.transition(StateOpen)
}

func (b *Breaker) transition(to State) {
	from := b.state
	b.state = to
	b.generation++
	if to != StateHalfOpen {
		b.probing = false
	}
	if b.onChange != nil {
		b.onChange(from, to)
	}
}
