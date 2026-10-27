package team

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/history"
	"github.com/secunda/team-tasks-service/internal/platform"
)

const maxNameLen = 120

// Store — то, что сервису нужно от хранилища команд.
type Store interface {
	// CreateTeam в одной транзакции создаёт команду, членство владельца (t.CreatedBy, роль owner)
	// и служебную ревизию кеша списков задач. Любая ошибка откатывает всё.
	CreateTeam(ctx context.Context, t Team) error
	// ListTeams возвращает команды пользователя с его ролью, порядок id ASC.
	ListTeams(ctx context.Context, userID uuid.UUID) ([]Team, error)
	// ListMembers возвращает состав, порядок user_id ASC, если userID состоит в команде;
	// иначе (команды нет или пользователь не участник) — ErrNotFound.
	ListMembers(ctx context.Context, teamID, userID uuid.UUID) ([]Member, error)
	// InMembershipTx выполняет fn в транзакции изменения состава: первым шагом строка команды
	// блокируется эксклюзивно (FOR UPDATE), что ждёт идущие записи задач команды и не пускает новые.
	// Если команды нет — ErrNotFound. Ошибка fn откатывает транзакцию.
	InMembershipTx(ctx context.Context, teamID uuid.UUID, fn func(MembershipTx) error) error
}

// MembershipTx — операции внутри транзакции изменения состава одной команды.
type MembershipTx interface {
	// Role возвращает актуальную роль пользователя; false — не участник.
	Role(ctx context.Context, userID uuid.UUID) (Role, bool, error)
	UserExists(ctx context.Context, userID uuid.UUID) (bool, error)
	AddMember(ctx context.Context, userID uuid.UUID, role Role, at time.Time) error
	SetRole(ctx context.Context, userID uuid.UUID, role Role) error
	// AssignedTaskIDs блокирует (FOR UPDATE) и возвращает задачи команды, назначенные userID,
	// в любом статусе, порядок id ASC.
	AssignedTaskIDs(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error)
	// ReleaseTask снимает исполнителя задачи: assignee_id = NULL, version + 1, updated_at = at,
	// и записывает журнал ревизии с автором actor и изменениями changes.
	ReleaseTask(ctx context.Context, taskID, historyID, actor uuid.UUID, changes json.RawMessage, at time.Time) error
	RemoveMember(ctx context.Context, userID uuid.UUID) error
	// BumpListRevision сбрасывает кешированные списки задач команды.
	BumpListRevision(ctx context.Context) error
}

// Service реализует сценарии команд. Права всегда читаются из MySQL, не из токена.
type Service struct {
	store Store
	ids   platform.IDGenerator
	clock platform.Clock
}

func NewService(store Store, ids platform.IDGenerator, clock platform.Clock) *Service {
	return &Service{store: store, ids: ids, clock: clock}
}

// Create создаёт команду; создатель становится её единственным owner.
func (s *Service) Create(ctx context.Context, actor uuid.UUID, name string) (Team, error) {
	switch {
	case strings.TrimSpace(name) == "":
		return Team{}, &platform.ValidationError{Fields: map[string]string{"name": "обязательное поле"}}
	case utf8.RuneCountInString(name) > maxNameLen:
		return Team{}, &platform.ValidationError{Fields: map[string]string{"name": "не более 120 символов"}}
	}
	id, err := s.ids.NewID()
	if err != nil {
		return Team{}, fmt.Errorf("create team: %w", err)
	}
	t := Team{ID: id, Name: name, CreatedBy: actor, CreatedAt: s.now(), Role: RoleOwner}
	if err := s.store.CreateTeam(ctx, t); err != nil {
		return Team{}, err
	}
	return t, nil
}

// List возвращает только команды текущего пользователя.
func (s *Service) List(ctx context.Context, actor uuid.UUID) ([]Team, error) {
	return s.store.ListTeams(ctx, actor)
}

// Members возвращает состав команды только её участникам; остальным — ErrNotFound.
func (s *Service) Members(ctx context.Context, teamID, actor uuid.UUID) ([]Member, error) {
	return s.store.ListMembers(ctx, teamID, actor)
}

// Invite немедленно добавляет зарегистрированного пользователя с ролью member.
// Порядок проверок: участие отправителя (404) → его роль (403) → приглашаемый (404/409).
// Так неучастник не узнаёт ни о составе команды, ни о существовании пользователя.
func (s *Service) Invite(ctx context.Context, teamID, actor, userID uuid.UUID) (Membership, error) {
	err := s.store.InMembershipTx(ctx, teamID, func(tx MembershipTx) error {
		if err := requireRole(ctx, tx, actor, CanInvite); err != nil {
			return err
		}
		exists, err := tx.UserExists(ctx, userID)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if _, member, err := tx.Role(ctx, userID); err != nil {
			return err
		} else if member {
			return ErrAlreadyMember
		}
		return tx.AddMember(ctx, userID, RoleMember, s.now())
	})
	if err != nil {
		return Membership{}, err
	}
	return Membership{TeamID: teamID, UserID: userID, Role: RoleMember}, nil
}

// ChangeRole назначает участнику admin или member. Доступно только owner; роль owner неизменна.
func (s *Service) ChangeRole(ctx context.Context, teamID, actor, userID uuid.UUID, role Role) (Membership, error) {
	if !IsAssignableRole(role) {
		return Membership{}, &platform.ValidationError{Fields: map[string]string{"role": "допустимы только admin и member"}}
	}
	err := s.store.InMembershipTx(ctx, teamID, func(tx MembershipTx) error {
		if err := requireRole(ctx, tx, actor, CanChangeRoles); err != nil {
			return err
		}
		current, member, err := tx.Role(ctx, userID)
		if err != nil {
			return err
		}
		if !member {
			return ErrNotFound
		}
		if current == RoleOwner {
			return ErrOwnerRoleImmutable
		}
		if current == role {
			return nil
		}
		return tx.SetRole(ctx, userID, role)
	})
	if err != nil {
		return Membership{}, err
	}
	return Membership{TeamID: teamID, UserID: userID, Role: role}, nil
}

// Remove исключает участника. Порядок проверок под эксклюзивной блокировкой команды: отправитель
// (404/403, в том числе при повторе) → цель уже не участник (nil: повтор успешного исключения, без
// изменений) → цель owner (422) → исключение себя (422) → назначения. Если назначения есть, а
// confirm не передан — AssignedTasksError без изменений. С confirm в той же транзакции снимаются
// все назначения на момент этого запроса (включая закрытые задачи) с ростом version и записью
// истории от имени actor, удаляется членство и сбрасываются кешированные списки команды.
// Учётная запись, авторство задач и комментариев, членства в других командах сохраняются.
func (s *Service) Remove(ctx context.Context, teamID, actor, userID uuid.UUID, confirm bool) error {
	return s.store.InMembershipTx(ctx, teamID, func(tx MembershipTx) error {
		if err := requireRole(ctx, tx, actor, CanRemoveMembers); err != nil {
			return err
		}
		role, member, err := tx.Role(ctx, userID)
		switch {
		case err != nil:
			return err
		case !member:
			return nil
		case role == RoleOwner:
			return ErrOwnerCannotBeRemoved
		case userID == actor:
			return ErrSelfRemoval
		}
		ids, err := tx.AssignedTaskIDs(ctx, userID)
		if err != nil {
			return err
		}
		if len(ids) > 0 && !confirm {
			return &AssignedTasksError{Count: len(ids)}
		}
		changes, err := history.Changes{
			history.AssigneeID: {Old: history.ID(&userID), New: nil},
		}.JSON()
		if err != nil {
			return err
		}
		now := s.now()
		for _, id := range ids {
			hid, err := s.ids.NewID()
			if err != nil {
				return fmt.Errorf("remove member: %w", err)
			}
			if err := tx.ReleaseTask(ctx, id, hid, actor, changes, now); err != nil {
				return err
			}
		}
		if err := tx.RemoveMember(ctx, userID); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil // списки задач не изменились
		}
		return tx.BumpListRevision(ctx)
	})
}

// requireRole читает актуальную роль под блокировкой команды: неучастник — 404, нехватка прав — 403.
func requireRole(ctx context.Context, tx MembershipTx, actor uuid.UUID, allowed func(Role) bool) error {
	role, member, err := tx.Role(ctx, actor)
	if err != nil {
		return err
	}
	if !member {
		return ErrNotFound
	}
	if !allowed(role) {
		return ErrForbidden
	}
	return nil
}

// now — время в UTC с точностью DATETIME(6).
func (s *Service) now() time.Time { return s.clock.Now().UTC().Truncate(time.Microsecond) }
