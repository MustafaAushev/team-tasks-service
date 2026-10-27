// Package team содержит бизнес-правила команд: роли, матрицу полномочий, создание команды,
// видимость состава, приглашение по ID и смену ролей.
package team

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Role — роль пользователя внутри одной команды. Глобальных ролей нет.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
)

// ParseRole принимает только известные роли.
func ParseRole(s string) (Role, bool) {
	switch r := Role(s); r {
	case RoleOwner, RoleAdmin, RoleMember:
		return r, true
	}
	return "", false
}

// Матрица полномочий команды. Owner задаётся только при создании команды и неизменен.

// CanInvite: приглашать (немедленно добавлять как member) могут owner и admin.
func CanInvite(actor Role) bool { return actor == RoleOwner || actor == RoleAdmin }

// CanChangeRoles: роли меняет только owner; admin не меняет роли никому.
func CanChangeRoles(actor Role) bool { return actor == RoleOwner }

// CanManageAllTasks: owner и admin изменяют любые задачи команды.
func CanManageAllTasks(actor Role) bool { return actor == RoleOwner || actor == RoleAdmin }

// CanViewStats: отчёт команды доступен owner и admin; member получает 403.
func CanViewStats(actor Role) bool { return actor == RoleOwner || actor == RoleAdmin }

// CanRemoveMembers: исключают участников owner и admin; владельца исключить нельзя никому.
func CanRemoveMembers(actor Role) bool { return actor == RoleOwner || actor == RoleAdmin }

// IsAssignableRole: сменой роли назначаются только admin и member, но не owner.
func IsAssignableRole(r Role) bool { return r == RoleAdmin || r == RoleMember }

var (
	// ErrNotFound — команды нет либо текущий пользователь в ней не состоит; также отсутствующий
	// приглашаемый или участник. Чужая команда неотличима от несуществующей.
	ErrNotFound = errors.New("team: not found")
	// ErrForbidden — пользователь состоит в команде, но его роли недостаточно.
	ErrForbidden = errors.New("team: forbidden")
	// ErrAlreadyMember — приглашаемый уже состоит в команде.
	ErrAlreadyMember = errors.New("team: already a member")
	// ErrOwnerRoleImmutable — роль владельца изменить нельзя.
	ErrOwnerRoleImmutable = errors.New("team: owner role is immutable")
	// ErrOwnerCannotBeRemoved — владельца нельзя исключить из команды.
	ErrOwnerCannotBeRemoved = errors.New("team: owner cannot be removed")
	// ErrSelfRemoval — самостоятельный выход (исключение самого себя) не поддерживается.
	ErrSelfRemoval = errors.New("team: self removal is not supported")
)

// AssignedTasksError — у исключаемого есть назначенные задачи команды, а снятие назначений не
// подтверждено. Ничего не изменено; Count — число назначений на момент запроса.
type AssignedTasksError struct{ Count int }

func (e *AssignedTasksError) Error() string {
	return fmt.Sprintf("team: member has %d assigned tasks", e.Count)
}

// Team — команда с ролью текущего пользователя в ней.
type Team struct {
	ID        uuid.UUID
	Name      string
	CreatedBy uuid.UUID
	CreatedAt time.Time
	Role      Role
}

// Member — участник команды.
type Member struct {
	UserID uuid.UUID
	Name   string
	Role   Role
}

// Membership — членство пользователя в команде.
type Membership struct {
	TeamID uuid.UUID
	UserID uuid.UUID
	Role   Role
}
