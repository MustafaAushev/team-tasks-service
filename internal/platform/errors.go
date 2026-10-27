package platform

// ValidationError содержит ошибки по полям; соответствует HTTP 422. Общий для всех доменов.
type ValidationError struct{ Fields map[string]string }

func (e *ValidationError) Error() string { return "validation failed" }
