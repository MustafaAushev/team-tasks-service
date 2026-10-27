package httpapi

import (
	"encoding/json"
	"net/http"
)

type errorBody struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	RequestID string         `json:"request_id"`
	Details   map[string]any `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// writeError отправляет единый формат ошибки и запоминает код/текст для итогового лога запроса.
func writeError(w http.ResponseWriter, r *http.Request, e *apiError) {
	info := infoFrom(r.Context())
	info.setError(e.Code, e.Message)
	for k, v := range e.Headers {
		w.Header().Set(k, v)
	}
	writeJSON(w, e.Status, errorBody{
		Code:      e.Code,
		Message:   e.Message,
		RequestID: info.RequestID,
		Details:   e.Details,
	})
}
