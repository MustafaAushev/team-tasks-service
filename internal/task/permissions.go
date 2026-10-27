package task

import (
	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/history"
	"github.com/secunda/team-tasks-service/internal/team"
)

// Изменяемые клиентом поля задачи. Остальные атрибуты (id, team_id, created_by, время, version)
// задаёт только сервер. Имена совпадают с ключами журнала.
const (
	FieldTitle       = history.Title
	FieldDescription = history.Description
	FieldStatus      = history.Status
	FieldAssigneeID  = history.AssigneeID
)

// CanUpdateFields проверяет право на КАЖДОЕ переданное поле, независимо от того, меняется ли его
// значение: исполнитель не может передать title, даже совпадающий с текущим.
//
//   - owner и admin — любые поля любой задачи команды;
//   - member-автор — все четыре поля своей задачи (право автора приоритетнее права исполнителя,
//     когда он одновременно исполнитель);
//   - member-исполнитель чужой задачи — только status;
//   - прочие участники — ничего.
func CanUpdateFields(role team.Role, actor uuid.UUID, t Task, fields []string) bool {
	if team.CanManageAllTasks(role) || t.CreatedBy == actor {
		return true
	}
	if t.AssigneeID == nil || *t.AssigneeID != actor {
		return false
	}
	for _, f := range fields {
		if f != FieldStatus {
			return false
		}
	}
	return true
}
