package httpapi

import (
	"context"
	"sync"

	"github.com/google/uuid"
)

type ctxKey int

const (
	keyInfo ctxKey = iota
	keyIdentity
	keyPathIDs
)

// requestInfo накапливает сведения о запросе для итогового лога. Заполняется по ходу обработки.
type requestInfo struct {
	mu        sync.Mutex
	RequestID string
	UserID    uuid.UUID
	TeamID    uuid.UUID
	TaskID    uuid.UUID
	ErrCode   string
	ErrText   string
}

func (i *requestInfo) setError(code, text string) {
	i.mu.Lock()
	i.ErrCode, i.ErrText = code, text
	i.mu.Unlock()
}

func (i *requestInfo) setUser(id uuid.UUID) {
	i.mu.Lock()
	i.UserID = id
	i.mu.Unlock()
}

// setTeam и setTask запоминают ID для логов: из параметров пути, тела, query или результата операции.
func (i *requestInfo) setTeam(id uuid.UUID) {
	i.mu.Lock()
	i.TeamID = id
	i.mu.Unlock()
}

func (i *requestInfo) setTask(id uuid.UUID) {
	i.mu.Lock()
	i.TaskID = id
	i.mu.Unlock()
}

func infoFrom(ctx context.Context) *requestInfo {
	if i, ok := ctx.Value(keyInfo).(*requestInfo); ok {
		return i
	}
	return &requestInfo{}
}

// UserID возвращает пользователя, аутентифицированного по JWT.
func UserID(ctx context.Context) (uuid.UUID, bool) {
	id, ok := ctx.Value(keyIdentity).(uuid.UUID)
	return id, ok
}

// PathID возвращает UUIDv7 из параметра пути, уже проверенный маршрутизатором и приведённый
// к нижнему регистру. Для маршрутов без этого параметра возвращается false.
func PathID(ctx context.Context, name string) (uuid.UUID, bool) {
	m, _ := ctx.Value(keyPathIDs).(map[string]uuid.UUID)
	id, ok := m[name]
	return id, ok
}
