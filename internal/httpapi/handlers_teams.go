package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/secunda/team-tasks-service/internal/platform"
	"github.com/secunda/team-tasks-service/internal/team"
)

type teamResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedBy string `json:"created_by"`
	CreatedAt string `json:"created_at"`
	Role      string `json:"role"`
}

func toTeamResponse(t team.Team) teamResponse {
	return teamResponse{
		ID:        platform.IDString(t.ID),
		Name:      t.Name,
		CreatedBy: platform.IDString(t.CreatedBy),
		CreatedAt: formatTime(t.CreatedAt),
		Role:      string(t.Role),
	}
}

type memberResponse struct {
	UserID string `json:"user_id"`
	Name   string `json:"name"`
	Role   string `json:"role"`
}

type membershipResponse struct {
	TeamID string `json:"team_id"`
	UserID string `json:"user_id"`
	Role   string `json:"role"`
}

func toMembershipResponse(m team.Membership) membershipResponse {
	return membershipResponse{TeamID: platform.IDString(m.TeamID), UserID: platform.IDString(m.UserID), Role: string(m.Role)}
}

func (h *handlers) createTeam(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name *string `json:"name"`
	}
	if aerr := decodeStrict(w, r, &req); aerr != nil {
		writeError(w, r, aerr)
		return
	}
	if req.Name == nil {
		writeError(w, r, errField("name", "обязательное поле"))
		return
	}
	t, err := h.teams.Create(r.Context(), identity(r), *req.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	infoFrom(r.Context()).setTeam(t.ID)
	writeJSON(w, http.StatusCreated, toTeamResponse(t))
}

func (h *handlers) listTeams(w http.ResponseWriter, r *http.Request) {
	teams, err := h.teams.List(r.Context(), identity(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]teamResponse, 0, len(teams))
	for _, t := range teams {
		items = append(items, toTeamResponse(t))
	}
	writeJSON(w, http.StatusOK, listResponse{Items: items})
}

func (h *handlers) listMembers(w http.ResponseWriter, r *http.Request) {
	members, err := h.teams.Members(r.Context(), pathID(r, "id"), identity(r))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]memberResponse, 0, len(members))
	for _, m := range members {
		items = append(items, memberResponse{UserID: platform.IDString(m.UserID), Name: m.Name, Role: string(m.Role)})
	}
	writeJSON(w, http.StatusOK, listResponse{Items: items})
}

// inviteMember принимает только user_id: роль при приглашении не передаётся (поле role — 422),
// приглашённый всегда member, admin назначается отдельной сменой роли.
func (h *handlers) inviteMember(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID *string `json:"user_id"`
	}
	if aerr := decodeStrict(w, r, &req); aerr != nil {
		writeError(w, r, aerr)
		return
	}
	fields := map[string]string{}
	userID := parseBodyID("user_id", req.UserID, true, fields)
	if len(fields) > 0 {
		writeError(w, r, errValidation(fields))
		return
	}
	m, err := h.teams.Invite(r.Context(), pathID(r, "id"), identity(r), *userID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toMembershipResponse(m))
}

func (h *handlers) changeRole(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role *string `json:"role"`
	}
	if aerr := decodeStrict(w, r, &req); aerr != nil {
		writeError(w, r, aerr)
		return
	}
	if req.Role == nil {
		writeError(w, r, errField("role", "обязательное поле"))
		return
	}
	role, ok := team.ParseRole(*req.Role)
	if !ok {
		writeError(w, r, errField("role", "допустимы только admin и member"))
		return
	}
	m, err := h.teams.ChangeRole(r.Context(), pathID(r, "id"), identity(r), pathID(r, "user_id"), role)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toMembershipResponse(m))
}

// removeMember исключает участника. Тело необязательно: без него или с confirm_unassign=false при
// назначенных задачах — 409 с их числом; {"confirm_unassign": true} снимает назначения и исключает.
func (h *handlers) removeMember(w http.ResponseWriter, r *http.Request) {
	body, aerr := readBody(w, r)
	if aerr != nil {
		writeError(w, r, aerr)
		return
	}
	confirm := false
	if len(bytes.TrimSpace(body)) > 0 {
		var req struct {
			ConfirmUnassign json.RawMessage `json:"confirm_unassign"`
		}
		if aerr := decodeBytes(body, &req); aerr != nil {
			writeError(w, r, aerr)
			return
		}
		// Поле необязательно, но если передано — только true или false, не null и не строка.
		if v := req.ConfirmUnassign; len(v) > 0 && (string(v) == "null" || json.Unmarshal(v, &confirm) != nil) {
			writeError(w, r, errField("confirm_unassign", "ожидается true или false"))
			return
		}
	}
	if err := h.teams.Remove(r.Context(), pathID(r, "id"), identity(r), pathID(r, "user_id"), confirm); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
