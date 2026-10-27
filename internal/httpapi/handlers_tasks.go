package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/task"
)

type taskResponse struct {
	ID          string  `json:"id"`
	TeamID      string  `json:"team_id"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	Status      string  `json:"status"`
	CreatedBy   string  `json:"created_by"`
	AssigneeID  *string `json:"assignee_id"`
	Version     int64   `json:"version"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	ClosedAt    *string `json:"closed_at"`
}

func toTaskResponse(t task.Task) taskResponse {
	return taskResponse{
		ID:          platform.IDString(t.ID),
		TeamID:      platform.IDString(t.TeamID),
		Title:       t.Title,
		Description: t.Description,
		Status:      string(t.Status),
		CreatedBy:   platform.IDString(t.CreatedBy),
		AssigneeID:  idPtr(t.AssigneeID),
		Version:     t.Version,
		CreatedAt:   formatTime(t.CreatedAt),
		UpdatedAt:   formatTime(t.UpdatedAt),
		ClosedAt:    formatTimePtr(t.ClosedAt),
	}
}

type historyResponse struct {
	ID          string          `json:"id"`
	TaskID      string          `json:"task_id"`
	ChangedBy   string          `json:"changed_by"`
	TaskVersion int64           `json:"task_version"`
	Changes     json.RawMessage `json:"changes"`
	CreatedAt   string          `json:"created_at"`
}

type commentResponse struct {
	ID        string `json:"id"`
	TaskID    string `json:"task_id"`
	UserID    string `json:"user_id"`
	Content   string `json:"content"`
	CreatedAt string `json:"created_at"`
}

func toCommentResponse(c task.Comment) commentResponse {
	return commentResponse{
		ID:        platform.IDString(c.ID),
		TaskID:    platform.IDString(c.TaskID),
		UserID:    platform.IDString(c.UserID),
		Content:   c.Content,
		CreatedAt: formatTime(c.CreatedAt),
	}
}

// createTask: version клиентом не передаётся (неизвестное поле — 422), сервер ставит 1.
func (h *handlers) createTask(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TeamID      *string `json:"team_id"`
		Title       *string `json:"title"`
		Description *string `json:"description"`
		Status      *string `json:"status"`
		AssigneeID  *string `json:"assignee_id"`
	}
	if aerr := decodeStrict(w, r, &req); aerr != nil {
		writeError(w, r, aerr)
		return
	}
	fields := map[string]string{}
	teamID := parseBodyID("team_id", req.TeamID, true, fields)
	assignee := parseBodyID("assignee_id", req.AssigneeID, false, fields)
	if req.Title == nil {
		fields["title"] = "обязательное поле"
	}
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	in := task.CreateInput{TeamID: *teamID, Title: *req.Title, AssigneeID: assignee}
	if req.Description != nil {
		in.Description = *req.Description
	}
	if req.Status != nil {
		in.Status = *req.Status
		if in.Status == "" {
			writeError(w, r, errField("status", "допустимы todo, in_progress, done"))
			return
		}
	}
	infoFrom(r.Context()).setTeam(in.TeamID)
	t, err := h.tasks.Create(r.Context(), identity(r), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	infoFrom(r.Context()).setTask(t.ID)
	writeJSON(w, http.StatusCreated, toTaskResponse(t))
}

func (h *handlers) getTask(w http.ResponseWriter, r *http.Request) {
	t, err := h.tasks.Get(r.Context(), identity(r), pathID(r, "id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	infoFrom(r.Context()).setTeam(t.TeamID)
	writeJSON(w, http.StatusOK, toTaskResponse(t))
}

func (h *handlers) listTasks(w http.ResponseWriter, r *http.Request) {
	q, fields := r.URL.Query(), map[string]string{}
	query := task.ListQuery{Limit: 20}

	raw, ok := queryValue(q, "team_id", fields)
	if !ok && fields["team_id"] == "" {
		fields["team_id"] = "обязательный параметр"
	} else if ok {
		if id := parseBodyID("team_id", &raw, true, fields); id != nil {
			query.TeamID = *id
		}
	}
	if v, ok := queryValue(q, "status", fields); ok {
		query.Status = v
		if v == "" {
			fields["status"] = "допустимы todo, in_progress, done"
		}
	}
	if v, ok := queryValue(q, "assignee_id", fields); ok {
		query.AssigneeID = parseBodyID("assignee_id", &v, true, fields)
	}
	if v, ok := queryValue(q, "pagination", fields); ok {
		query.Mode = v
		if v == "" {
			fields["pagination"] = "допустимы offset и cursor"
		}
	}
	if n, ok := queryInt(q, "limit", fields); ok {
		query.Limit = n
	}
	if n, ok := queryInt(q, "offset", fields); ok {
		query.Offset = &n
	}
	query.Cursor, _ = queryValue(q, "cursor", fields)
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}

	infoFrom(r.Context()).setTeam(query.TeamID)
	page, err := h.tasks.List(r.Context(), identity(r), query)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]taskResponse, 0, len(page.Items))
	for _, t := range page.Items {
		items = append(items, toTaskResponse(t))
	}
	resp := listResponse{Items: items}
	if page.Mode == task.PageCursor {
		resp.Pagination = cursorPagination{Mode: page.Mode, Limit: page.Limit, NextCursor: page.NextCursor}
	} else {
		resp.Pagination = offsetPagination{Mode: page.Mode, Limit: page.Limit, Offset: page.Offset, Total: page.Total}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handlers) updateTask(w http.ResponseWriter, r *http.Request) {
	in, aerr := readUpdateTask(w, r)
	if aerr != nil {
		writeError(w, r, aerr)
		return
	}
	t, err := h.tasks.Update(r.Context(), identity(r), pathID(r, "id"), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	infoFrom(r.Context()).setTeam(t.TeamID)
	writeJSON(w, http.StatusOK, toTaskResponse(t))
}

func (h *handlers) taskHistory(w http.ResponseWriter, r *http.Request) {
	limit, cursor, aerr := cursorQuery(r)
	if aerr != nil {
		writeError(w, r, aerr)
		return
	}
	page, err := h.tasks.History(r.Context(), identity(r), pathID(r, "id"), limit, cursor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]historyResponse, 0, len(page.Items))
	for _, e := range page.Items {
		items = append(items, historyResponse{
			ID:          platform.IDString(e.ID),
			TaskID:      platform.IDString(e.TaskID),
			ChangedBy:   platform.IDString(e.ChangedBy),
			TaskVersion: e.TaskVersion,
			Changes:     e.Changes,
			CreatedAt:   formatTime(e.CreatedAt),
		})
	}
	writeJSON(w, http.StatusOK, listResponse{Items: items,
		Pagination: cursorPagination{Mode: task.PageCursor, Limit: page.Limit, NextCursor: page.NextCursor}})
}

func (h *handlers) createComment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Content *string `json:"content"`
	}
	if aerr := decodeStrict(w, r, &req); aerr != nil {
		writeError(w, r, aerr)
		return
	}
	if req.Content == nil {
		writeError(w, r, errField("content", "обязательное поле"))
		return
	}
	c, err := h.tasks.AddComment(r.Context(), identity(r), pathID(r, "id"), *req.Content)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toCommentResponse(c))
}

func (h *handlers) listComments(w http.ResponseWriter, r *http.Request) {
	limit, cursor, aerr := cursorQuery(r)
	if aerr != nil {
		writeError(w, r, aerr)
		return
	}
	page, err := h.tasks.Comments(r.Context(), identity(r), pathID(r, "id"), limit, cursor)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]commentResponse, 0, len(page.Items))
	for _, c := range page.Items {
		items = append(items, toCommentResponse(c))
	}
	writeJSON(w, http.StatusOK, listResponse{Items: items,
		Pagination: cursorPagination{Mode: task.PageCursor, Limit: page.Limit, NextCursor: page.NextCursor}})
}
