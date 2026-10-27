package httpapi

import (
	"net/http"
	"time"

	"github.com/secunda/team-tasks-service/internal/auth"
	"github.com/secunda/team-tasks-service/internal/platform"
)

// publicUser — публичный профиль: без пароля и хеша.
type publicUser struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

func toPublicUser(u auth.User) publicUser {
	return publicUser{
		ID:        platform.IDString(u.ID),
		Email:     u.Email,
		Name:      u.Name,
		CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
}

type registerRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	AccessToken string     `json:"access_token"`
	TokenType   string     `json:"token_type"`
	ExpiresIn   int64      `json:"expires_in"`
	User        publicUser `json:"user"`
}

func (h *handlers) register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if aerr := decodeStrict(w, r, &req); aerr != nil {
		writeError(w, r, aerr)
		return
	}
	u, err := h.auth.Register(r.Context(), auth.RegisterInput{Email: req.Email, Password: req.Password, Name: req.Name})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPublicUser(u))
}

func (h *handlers) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if aerr := decodeStrict(w, r, &req); aerr != nil {
		writeError(w, r, aerr)
		return
	}
	res, err := h.auth.Login(r.Context(), req.Email, req.Password)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	infoFrom(r.Context()).setUser(res.User.ID)
	writeJSON(w, http.StatusOK, loginResponse{
		AccessToken: res.Token,
		TokenType:   "Bearer",
		ExpiresIn:   res.ExpiresIn,
		User:        toPublicUser(res.User),
	})
}
