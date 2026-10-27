package platform

import (
	"io"
	"log/slog"
	"strings"
)

// NewLogger создаёт JSON-логгер: верхние ключи timestamp, level, message (переименованы из
// стандартных time/msg; level в нижнем регистре: info, error), затем context, request_id,
// известные ID и meta (JSON-объект).
// Время — RFC 3339 в UTC с точностью до наносекунд.
func NewLogger(w io.Writer, level slog.Level) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) > 0 {
				return a
			}
			switch a.Key {
			case slog.TimeKey:
				return slog.String("timestamp", a.Value.Time().UTC().Format("2006-01-02T15:04:05.000000000Z07:00"))
			case slog.MessageKey:
				a.Key = "message"
			case slog.LevelKey:
				a.Value = slog.StringValue(strings.ToLower(a.Value.Any().(slog.Level).String()))
			}
			return a
		},
	})
	return slog.New(h)
}

// DiscardLogger возвращает логгер, ничего не записывающий; для тестов.
func DiscardLogger() *slog.Logger {
	return NewLogger(io.Discard, slog.LevelError+100)
}
