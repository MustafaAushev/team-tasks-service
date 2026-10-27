// Package api содержит контракт HTTP API, встроенный в бинарный файл.
package api

import _ "embed"

// OpenAPISpec — содержимое openapi.yaml (OpenAPI 3.0.3), источник истины интерфейса.
//
//go:embed openapi.yaml
var OpenAPISpec []byte
