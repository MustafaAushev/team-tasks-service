package httpapi

import (
	"net/http"

	"github.com/secunda/team-tasks-service/internal/platform"
)

type statsResponse struct {
	AsOf                 string             `json:"as_of"`
	TasksByStatus        statusCounts       `json:"tasks_by_status"`
	TopAssignees         []topAssignee      `json:"top_assignees"`
	AverageCloseTimeDays *float64           `json:"average_close_time_days"`
	CommentsTotal        int                `json:"comments_total"`
	CommentsByTask       []taskCommentCount `json:"comments_by_task"`
}

type statusCounts struct {
	Todo       int `json:"todo"`
	InProgress int `json:"in_progress"`
	Done       int `json:"done"`
}

type topAssignee struct {
	UserID           string `json:"user_id"`
	Name             string `json:"name"`
	ClosedTasksCount int    `json:"closed_tasks_count"`
}

type taskCommentCount struct {
	TaskID        string `json:"task_id"`
	CommentsCount int    `json:"comments_count"`
}

func (h *handlers) teamStats(w http.ResponseWriter, r *http.Request) {
	rep, err := h.stats.Team(r.Context(), pathID(r, "team_id"), identity(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	resp := statsResponse{
		AsOf: formatTime(rep.AsOf),
		TasksByStatus: statusCounts{
			Todo: rep.ByStatus["todo"], InProgress: rep.ByStatus["in_progress"], Done: rep.ByStatus["done"],
		},
		TopAssignees:         make([]topAssignee, 0, len(rep.TopAssignees)),
		AverageCloseTimeDays: rep.AvgCloseDays,
		CommentsTotal:        rep.CommentsTotal,
		CommentsByTask:       make([]taskCommentCount, 0, len(rep.CommentsByTask)),
	}
	for _, a := range rep.TopAssignees {
		resp.TopAssignees = append(resp.TopAssignees, topAssignee{UserID: platform.IDString(a.UserID), Name: a.Name, ClosedTasksCount: a.ClosedTasks})
	}
	for _, c := range rep.CommentsByTask {
		resp.CommentsByTask = append(resp.CommentsByTask, taskCommentCount{TaskID: platform.IDString(c.TaskID), CommentsCount: c.Comments})
	}
	writeJSON(w, http.StatusOK, resp)
}
