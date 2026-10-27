package task

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/team"
)

// Store — то, что сервису нужно от хранилища задач.
type Store interface {
	// TaskTeam возвращает команду задачи (ErrNotFound, если задачи нет). Используется только для
	// выбора блокируемой команды: права и diff по этому чтению не вычисляются.
	TaskTeam(ctx context.Context, taskID uuid.UUID) (uuid.UUID, error)
	// InTeamTx выполняет fn в бизнес-транзакции READ COMMITTED, которая первым шагом берёт строку
	// команды FOR SHARE и держит её до конца: изменения состава (FOR UPDATE) ждут эту запись, а
	// запись видит актуальные членство и роли. Команды нет — ErrNotFound. Ошибка fn — rollback.
	InTeamTx(ctx context.Context, teamID uuid.UUID, fn func(Tx) error) error

	// VisibleTask возвращает задачу, если userID состоит в её команде; иначе ErrNotFound.
	VisibleTask(ctx context.Context, taskID, userID uuid.UUID) (Task, error)
	// ListRevision проверяет актуальное членство userID (не участник — ErrNotFound) и возвращает
	// текущую ревизию списков задач команды — поколение ключей кеша.
	ListRevision(ctx context.Context, teamID, userID uuid.UUID) (int64, error)
	// ListOffset возвращает страницу, total и ревизию списков из одного согласованного снимка, в котором
	// проверено и членство userID (не участник — ErrNotFound). Порядок id DESC.
	ListOffset(ctx context.Context, userID uuid.UUID, f Filter, limit, offset int) ([]Task, int, int64, error)
	// ListBefore возвращает до limit задач с id < before (nil — с начала), порядок id DESC, и ревизию
	// списков из того же снимка; не участник — ErrNotFound.
	ListBefore(ctx context.Context, userID uuid.UUID, f Filter, before *uuid.UUID, limit int) ([]Task, int64, error)
	// History возвращает до limit записей с task_version > afterVersion, порядок task_version ASC.
	History(ctx context.Context, taskID uuid.UUID, afterVersion int64, limit int) ([]HistoryEntry, error)
	// Comments возвращает до limit комментариев с id > after (nil — с начала), порядок id ASC.
	Comments(ctx context.Context, taskID uuid.UUID, after *uuid.UUID, limit int) ([]Comment, error)
}

// Tx — операции внутри бизнес-транзакции одной команды (строка команды уже заблокирована).
type Tx interface {
	// MemberRole возвращает актуальную роль пользователя в команде; false — не участник.
	MemberRole(ctx context.Context, userID uuid.UUID) (team.Role, bool, error)
	// Task читает задачу команды обычным SELECT, без блокировки строки задачи.
	Task(ctx context.Context, taskID uuid.UUID) (Task, bool, error)
	InsertTask(ctx context.Context, t Task) error
	// UpdateIfVersion записывает состояние t и version+1 условным UPDATE
	// ... WHERE id = ? AND team_id = ? AND version = expected; false — строка не совпала.
	UpdateIfVersion(ctx context.Context, t Task, expected int64) (bool, error)
	// Version — текущая версия задачи; false — задачи нет.
	Version(ctx context.Context, taskID uuid.UUID) (int64, bool, error)
	InsertHistory(ctx context.Context, h HistoryEntry) error
	// BumpListRevision увеличивает ревизию кешированных списков задач команды.
	BumpListRevision(ctx context.Context) error
	InsertComment(ctx context.Context, c Comment) error
}

// Service реализует сценарии задач, журнала и комментариев.
type Service struct {
	store   Store
	ids     platform.IDGenerator
	clock   platform.Clock
	cursors cursorCodec
	cache   ListCache // nil — списки всегда читаются из MySQL
}

// NewService создаёт сервис; cursorSecret подписывает курсоры всех трёх списков.
func NewService(store Store, ids platform.IDGenerator, clock platform.Clock, cursorSecret []byte) *Service {
	return &Service{store: store, ids: ids, clock: clock, cursors: cursorCodec{secret: cursorSecret}}
}

// WithListCache включает кеш списков задач.
func (s *Service) WithListCache(c ListCache) *Service {
	s.cache = c
	return s
}

// Get возвращает задачу только участнику её команды.
func (s *Service) Get(ctx context.Context, actor, taskID uuid.UUID) (Task, error) {
	return s.store.VisibleTask(ctx, taskID, actor)
}

// requireAssignee: исполнитель должен состоять в команде на момент записи (под блокировкой команды).
func requireAssignee(ctx context.Context, tx Tx, assignee *uuid.UUID) error {
	if assignee == nil {
		return nil
	}
	_, member, err := tx.MemberRole(ctx, *assignee)
	if err != nil {
		return err
	}
	if !member {
		return ErrAssigneeNotInTeam
	}
	return nil
}

// now — время в UTC с точностью DATETIME(6).
func (s *Service) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }
