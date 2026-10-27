package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/auth"
	"github.com/secunda/team-tasks-service/internal/platform"
)

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// statusWriter запоминает итоговый статус ответа для лога.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// observe выставляет request_id, ограничивает время обработки, перехватывает panic и пишет
// одну итоговую запись на запрос. Тела запросов, Authorization и query в лог не попадают.
func observe(logger *slog.Logger, timeout time.Duration, route func(*http.Request) string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		info := &requestInfo{RequestID: requestID(r)}
		w.Header().Set("X-Request-ID", info.RequestID)

		ctx := platform.WithRequestID(context.WithValue(r.Context(), keyInfo, info), info.RequestID)
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		r = r.WithContext(ctx)

		sw := &statusWriter{ResponseWriter: w}
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					logger.LogAttrs(ctx, slog.LevelError, "panic recovered",
						slog.String("context", "http"),
						slog.String("request_id", info.RequestID),
						slog.Any("meta", map[string]any{"event": "panic", "panic": safePanic(rec), "stack": string(debug.Stack())}))
					if sw.status == 0 {
						writeError(sw, r, errInternal())
					}
				}
			}()
			next.ServeHTTP(sw, r)
		}()
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		logRequest(logger, ctx, r, info, sw.status, route(r), time.Since(start))
	})
}

func requestID(r *http.Request) string {
	if v := r.Header.Get("X-Request-ID"); requestIDPattern.MatchString(v) {
		return v
	}
	if id, err := uuid.NewV7(); err == nil {
		return id.String()
	}
	return uuid.NewString()
}

func safePanic(v any) string {
	if e, ok := v.(error); ok {
		return e.Error()
	}
	if s, ok := v.(string); ok {
		return s
	}
	return "panic"
}

// logRequest пишет итоговую запись: message — фактический путь без query (и безопасный текст
// ошибки), HTTP-поля — в meta. Любой статус >= 400 логируется на ERROR.
func logRequest(logger *slog.Logger, ctx context.Context, r *http.Request, info *requestInfo, status int, route string, d time.Duration) {
	info.mu.Lock()
	code, text := info.ErrCode, info.ErrText
	user, team, task := info.UserID, info.TeamID, info.TaskID
	info.mu.Unlock()

	level := slog.LevelInfo
	msg := r.URL.Path
	meta := map[string]any{
		"method":      r.Method,
		"route":       route,
		"status":      status,
		"duration_ms": float64(d.Microseconds()) / 1000,
	}
	if status >= 400 {
		level = slog.LevelError
		if code != "" {
			meta["error_code"] = code
		}
		if text != "" {
			msg += ": " + text
		}
	}

	attrs := []slog.Attr{
		slog.String("context", "http"),
		slog.String("request_id", info.RequestID),
	}
	if user != uuid.Nil {
		attrs = append(attrs, slog.String("user_id", platform.IDString(user)))
	}
	if team != uuid.Nil {
		attrs = append(attrs, slog.String("team_id", platform.IDString(team)))
	}
	if task != uuid.Nil {
		attrs = append(attrs, slog.String("task_id", platform.IDString(task)))
	}
	attrs = append(attrs, slog.Any("meta", meta))
	logger.LogAttrs(ctx, level, msg, attrs...)
}

// authenticated проверяет Bearer JWT и кладёт личность в контекст. Роли команды не читаются.
func authenticated(tokens *auth.TokenManager, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, raw, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(raw) == "" {
			writeError(w, r, errUnauthorized())
			return
		}
		id, err := tokens.Verify(strings.TrimSpace(raw))
		if err != nil {
			writeError(w, r, errUnauthorized())
			return
		}
		infoFrom(r.Context()).setUser(id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyIdentity, id)))
	})
}

// withPathIDs проверяет перечисленные параметры пути как UUIDv7 и нормализует регистр.
// Некорректное значение — 422 до любой бизнес-логики; корректный, но отсутствующий ID
// обрабатывается дальше обычными правилами 404/прав.
func withPathIDs(params []pathParam, next http.Handler) http.Handler {
	if len(params) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ids := make(map[string]uuid.UUID, len(params))
		info := infoFrom(r.Context())
		for _, p := range params {
			id, err := platform.ParseID(r.PathValue(p.name))
			if err != nil {
				writeError(w, r, errField(p.name, "ожидается UUID версии 7"))
				return
			}
			ids[p.name] = id
			switch p.entity {
			case entityTeam:
				info.setTeam(id)
			case entityTask:
				info.setTask(id)
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), keyPathIDs, ids)))
	})
}
