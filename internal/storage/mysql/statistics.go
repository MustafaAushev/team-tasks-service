package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/statistics"
	"github.com/secunda/team-tasks-service/internal/team"
)

// StatsStore реализует statistics.Store.
type StatsStore struct{ DB *sql.DB }

var _ statistics.Store = StatsStore{}

func (s StatsStore) MemberRole(ctx context.Context, teamID, userID uuid.UUID) (team.Role, bool, error) {
	role, ok, err := memberRole(ctx, s.DB, teamID, userID)
	return team.Role(role), ok, err
}

// reportSQL считает все метрики одним запросом и возвращает одну строку.
//
//   - base — задачи команды; читаются один раз по индексу team_id, остальные CTE строятся из неё.
//   - summary — задачи по статусам и среднее время закрытия текущих done в сутках. Длительность берётся
//     в микросекундах: TIMESTAMPDIFF(DAY) отбросил бы дробную часть суток.
//   - top_assignees — исполнители по задачам, закрытым в окне [from, asOf]; место — по числу закрытий,
//     при равенстве — по user_id.
//   - comment_total — общее число комментариев к задачам команды. Считается в отдельном CTE, поэтому
//     JOIN с комментариями не умножает строки остальных метрик; комментарии читаются по индексу задачи.
//
// Топ возвращается JSON-массивом. JSON_ARRAYAGG в MySQL не гарантирует порядок элементов, поэтому
// каждый элемент несёт позицию rn из SQL, а порядок восстанавливается при разборе.
// Метрики считаются по текущему состоянию задач (status, assignee_id, closed_at), не по журналу.
const reportSQL = `
WITH
base AS (
    SELECT id, status, assignee_id, created_at, closed_at FROM tasks WHERE team_id = ?
),
summary AS (
    SELECT
        COALESCE(SUM(status = 'todo'), 0)        AS todo,
        COALESCE(SUM(status = 'in_progress'), 0) AS in_progress,
        COALESCE(SUM(status = 'done'), 0)        AS done,
        ROUND(AVG(CASE WHEN status = 'done'
                       THEN TIMESTAMPDIFF(MICROSECOND, created_at, closed_at) END) / 86400000000, 6) AS avg_close_days
    FROM base
),
top_assignees AS (
    SELECT assignee_id, COUNT(*) AS closed_tasks,
           ROW_NUMBER() OVER (ORDER BY COUNT(*) DESC, assignee_id ASC) AS rn
    FROM base
    WHERE status = 'done' AND assignee_id IS NOT NULL AND closed_at BETWEEN ? AND ?
    GROUP BY assignee_id
),
comment_total AS (
    SELECT COUNT(*) AS comments FROM base b JOIN task_comments c ON c.task_id = b.id
)
SELECT
    s.todo,
    s.in_progress,
    s.done,
    s.avg_close_days,
    (SELECT COALESCE(JSON_ARRAYAGG(JSON_OBJECT(
                'user_id', BIN_TO_UUID(t.assignee_id), 'name', u.name, 'closed_tasks', t.closed_tasks, 'rn', t.rn)),
            JSON_ARRAY())
     FROM top_assignees t JOIN users u ON u.id = t.assignee_id
     WHERE t.rn <= 3) AS top_assignees,
    ct.comments AS comments_total
FROM summary s CROSS JOIN comment_total ct`

// ReportQuery возвращает текст и параметры запроса отчёта (для EXPLAIN в тестах).
func ReportQuery(teamID uuid.UUID, from, asOf time.Time) (string, []any) {
	return reportSQL, []any{platform.IDBytes(teamID), from, asOf}
}

// Элементы JSON-массивов отчёта.
type (
	topAssigneeRow struct {
		UserID      string `json:"user_id"`
		Name        string `json:"name"`
		ClosedTasks int    `json:"closed_tasks"`
		Rn          int    `json:"rn"`
	}
)

func (s StatsStore) Report(ctx context.Context, teamID uuid.UUID, from, asOf time.Time) (statistics.Report, error) {
	query, args := ReportQuery(teamID, from, asOf)
	var (
		todo, inProgress, done, commentsTotal int
		avgClose                              sql.NullFloat64
		topRaw                                []byte
	)
	if err := s.DB.QueryRowContext(ctx, query, args...).Scan(
		&todo, &inProgress, &done, &avgClose, &topRaw, &commentsTotal); err != nil {
		return statistics.Report{}, fmt.Errorf("team report: %w", err)
	}

	var top []topAssigneeRow
	if err := json.Unmarshal(topRaw, &top); err != nil {
		return statistics.Report{}, fmt.Errorf("team report top_assignees: %w", err)
	}
	sort.Slice(top, func(i, j int) bool { return top[i].Rn < top[j].Rn })

	r := statistics.Report{
		AsOf:          asOf,
		ByStatus:      map[string]int{"todo": todo, "in_progress": inProgress, "done": done},
		TopAssignees:  make([]statistics.Assignee, 0, len(top)),
		CommentsTotal: commentsTotal,
	}
	if avgClose.Valid {
		d := avgClose.Float64
		r.AvgCloseDays = &d
	}
	for _, a := range top {
		id, err := uuid.Parse(a.UserID)
		if err != nil {
			return statistics.Report{}, fmt.Errorf("team report user_id: %w", err)
		}
		r.TopAssignees = append(r.TopAssignees, statistics.Assignee{UserID: id, Name: a.Name, ClosedTasks: a.ClosedTasks})
	}
	return r, nil
}
