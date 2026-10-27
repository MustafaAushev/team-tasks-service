package mysql

import (
	"fmt"
	"io/fs"
	"strconv"
	"strings"

	"github.com/secunda/team-tasks-service/migrations"
)

// latestEmbedded вычисляет максимальную версию по именам файлов NNNNN_*.sql.
func latestEmbedded() (int64, error) {
	entries, err := fs.ReadDir(migrations.FS, ".")
	if err != nil {
		return 0, err
	}
	var latest int64
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(name, "_")
		if !ok {
			return 0, fmt.Errorf("migration %q: нет префикса версии", name)
		}
		v, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("migration %q: некорректная версия: %w", name, err)
		}
		if v > latest {
			latest = v
		}
	}
	return latest, nil
}
