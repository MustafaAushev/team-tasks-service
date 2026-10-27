package platform

import "context"

type requestIDKey struct{}

// WithRequestID кладёт ID корреляции HTTP-запроса в контекст: его читают логи слоёв ниже HTTP
// (например, кеш), не завися от пакета httpapi.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}

// RequestID возвращает ID корреляции для поля request_id лога: строку запроса или nil вне HTTP.
func RequestID(ctx context.Context) any {
	if id, ok := ctx.Value(requestIDKey{}).(string); ok && id != "" {
		return id
	}
	return nil
}
