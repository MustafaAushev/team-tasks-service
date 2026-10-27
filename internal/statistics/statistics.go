// Package statistics строит отчёт команды для owner и admin.
package statistics

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/team"
)

// TopWindow — окно top_assignees: закрытия в [as_of − 30 суток, as_of], обе границы включены.
const TopWindow = 30 * 24 * time.Hour

// Report — отчёт одной команды на момент AsOf.
type Report struct {
	AsOf         time.Time
	ByStatus     map[string]int // todo, in_progress, done — всегда все три ключа
	TopAssignees []Assignee     // не более трёх: count DESC, user_id ASC
	// AvgCloseDays — среднее created_at → closed_at текущих done в сутках по 24 часа,
	// 6 знаков; nil, если закрытых задач нет.
	AvgCloseDays   *float64
	CommentsTotal  int
	CommentsByTask []TaskComments // все задачи команды, включая нули, task_id ASC
}

type Assignee struct {
	UserID      uuid.UUID
	Name        string
	ClosedTasks int
}

type TaskComments struct {
	TaskID   uuid.UUID
	Comments int
}

// Store — то, что отчёту нужно от хранилища.
type Store interface {
	// MemberRole возвращает роль пользователя в команде; false — не участник или команды нет.
	MemberRole(ctx context.Context, teamID, userID uuid.UUID) (team.Role, bool, error)
	// Report вычисляет все метрики одним SQL-запросом на момент asOf; from — нижняя граница окна топа.
	Report(ctx context.Context, teamID uuid.UUID, from, asOf time.Time) (Report, error)
}

// Service выдаёт отчёт после проверки прав.
type Service struct {
	store Store
	clock platform.Clock
}

func NewService(store Store, clock platform.Clock) *Service {
	return &Service{store: store, clock: clock}
}

// Team возвращает отчёт команды: неучастник — team.ErrNotFound, member — team.ErrForbidden.
// Момент as_of берётся один раз и общий для всех метрик; точность — микросекунды, как у DATETIME(6).
func (s *Service) Team(ctx context.Context, teamID, actor uuid.UUID) (Report, error) {
	role, member, err := s.store.MemberRole(ctx, teamID, actor)
	if err != nil {
		return Report{}, err
	}
	if !member {
		return Report{}, team.ErrNotFound
	}
	if !team.CanViewStats(role) {
		return Report{}, team.ErrForbidden
	}
	asOf := s.clock.Now().UTC().Truncate(time.Microsecond)
	return s.store.Report(ctx, teamID, asOf.Add(-TopWindow), asOf)
}
