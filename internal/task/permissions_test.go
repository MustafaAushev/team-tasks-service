package task

import (
	"fmt"
	"testing"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/team"
)

// Матрица прав на поля задачи: роль × отношение к задаче × набор переданных полей.
// Право проверяется для каждого переданного поля, даже если его значение не меняется.
func TestTaskFieldPermissionMatrix(t *testing.T) {
	actor, other := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	ptr := func(id uuid.UUID) *uuid.UUID { return &id }

	relations := map[string]Task{
		"автор":                {CreatedBy: actor, AssigneeID: ptr(other)},
		"исполнитель":          {CreatedBy: other, AssigneeID: ptr(actor)},
		"автор и исполнитель":  {CreatedBy: actor, AssigneeID: ptr(actor)},
		"посторонний":          {CreatedBy: other, AssigneeID: ptr(other)},
		"посторонний, без исп": {CreatedBy: other},
	}
	fieldSets := map[string][]string{
		"status":             {FieldStatus},
		"title":              {FieldTitle},
		"description":        {FieldDescription},
		"assignee_id":        {FieldAssigneeID},
		"status+title":       {FieldStatus, FieldTitle},
		"все четыре":         {FieldTitle, FieldDescription, FieldStatus, FieldAssigneeID},
		"status+assignee":    {FieldStatus, FieldAssigneeID},
		"status+description": {FieldStatus, FieldDescription},
	}
	// Ожидание для member; owner и admin могут всё во всех случаях.
	memberAllowed := func(rel string, fields []string) bool {
		switch rel {
		case "автор", "автор и исполнитель":
			return true // право автора приоритетнее права исполнителя
		case "исполнитель":
			return len(fields) == 1 && fields[0] == FieldStatus
		default:
			return false
		}
	}

	for _, role := range []team.Role{team.RoleOwner, team.RoleAdmin, team.RoleMember} {
		for rel, task := range relations {
			for setName, fields := range fieldSets {
				want := role != team.RoleMember || memberAllowed(rel, fields)
				name := fmt.Sprintf("%s/%s/%s", role, rel, setName)
				if got := CanUpdateFields(role, actor, task, fields); got != want {
					t.Errorf("%s: получено %v, ожидалось %v", name, got, want)
				}
			}
		}
	}
}
