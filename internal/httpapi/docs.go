package httpapi

import (
	"embed"
	"io/fs"
	"net/http"
)

// Swagger UI (swagger-ui-dist 5.17.14, Apache-2.0, лицензия рядом с файлами) встроен в бинарный
// файл: документация открывается без доступа к внешним CDN.
//
//go:embed swagger
var swaggerFS embed.FS

// mountDocs регистрирует /openapi.yaml и Swagger UI на /docs/. Маршруты публичные.
func (a *API) mountDocs(mux *http.ServeMux) {
	assets, err := fs.Sub(swaggerFS, "swagger")
	if err != nil {
		panic(err) // путь встроен на этапе компиляции
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		panic(err)
	}

	mux.HandleFunc("GET /openapi.yaml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(a.spec)
	})
	mux.HandleFunc("GET /docs/{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(index)
	})
	mux.Handle("GET /docs/assets/", http.StripPrefix("/docs/assets/", http.FileServerFS(assets)))
}
