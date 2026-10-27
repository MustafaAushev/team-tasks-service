// Command migrate применяет и откатывает SQL-миграции. Запускается отдельно от HTTP-сервера.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/secunda/team-tasks-service/internal/config"
	"github.com/secunda/team-tasks-service/internal/storage/mysql"
)

const usage = `использование: migrate <up|down|version>

  up       применить все миграции (повторный запуск безопасен)
  down     откатить ВСЕ миграции: удаляет таблицы и данные, только для локального сброса
  version  показать текущую версию схемы`

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func run(cmd string) error {
	getenv, err := config.Getenv(config.DotEnvFile)
	if err != nil {
		return err
	}
	cfg, err := config.LoadMySQL(getenv)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db, err := mysql.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	switch cmd {
	case "up":
		if err := mysql.Up(ctx, db); err != nil {
			return err
		}
		return printVersion(ctx, db)
	case "down":
		if err := mysql.DownAll(ctx, db); err != nil {
			return err
		}
		return printVersion(ctx, db)
	case "version":
		return printVersion(ctx, db)
	default:
		return fmt.Errorf("неизвестная команда %q\n%s", cmd, usage)
	}
}

func printVersion(ctx context.Context, db *sql.DB) error {
	v, err := mysql.Version(ctx, db)
	if err != nil {
		return err
	}
	fmt.Println("версия схемы:", v)
	return nil
}
