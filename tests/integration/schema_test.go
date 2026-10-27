//go:build integration

package integration

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/secunda/team-tasks-service/internal/platform"
	mysqlstore "github.com/secunda/team-tasks-service/internal/storage/mysql"
	"github.com/secunda/team-tasks-service/internal/testutil"
)

func mustID(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := platform.UUIDv7Generator{}.NewID()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMigrationsUpUpDownUp(t *testing.T) {
	db, _ := testutil.MySQL(t) // уже применён первый up
	ctx := context.Background()

	latest, err := mysqlstore.LatestVersion()
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := mysqlstore.Version(ctx, db); v != latest {
		t.Fatalf("версия после up: %d, ожидалась %d", v, latest)
	}
	if err := mysqlstore.Up(ctx, db); err != nil {
		t.Fatalf("повторный up: %v", err)
	}
	if err := mysqlstore.DownAll(ctx, db); err != nil {
		t.Fatalf("down: %v", err)
	}
	if v, _ := mysqlstore.Version(ctx, db); v != 0 {
		t.Fatalf("версия после down: %d", v)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables
		WHERE table_schema = DATABASE() AND table_name <> 'goose_db_version'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 0 {
		t.Fatalf("после down осталось таблиц: %d", tables)
	}
	if err := mysqlstore.Up(ctx, db); err != nil {
		t.Fatalf("up после down: %v", err)
	}
	if v, _ := mysqlstore.Version(ctx, db); v != latest {
		t.Fatalf("версия после down/up: %d", v)
	}
}

func TestSchemaHasNoAutoIncrementAndUsesBinaryIDs(t *testing.T) {
	db, _ := testutil.MySQL(t)

	var autoInc int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name <> 'goose_db_version'
		  AND extra LIKE '%auto_increment%'`).Scan(&autoInc); err != nil {
		t.Fatal(err)
	}
	if autoInc != 0 {
		t.Fatalf("найдено AUTO_INCREMENT-колонок: %d", autoInc)
	}

	var defaults int
	if err := db.QueryRow(`SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = DATABASE()
		  AND (column_default LIKE '%uuid%' OR extra LIKE '%DEFAULT_GENERATED%uuid%')`).Scan(&defaults); err != nil {
		t.Fatal(err)
	}
	if defaults != 0 {
		t.Fatal("SQL-генерация UUID в DEFAULT не допускается")
	}

	idColumns := map[string][]string{
		"users":                    {"id"},
		"teams":                    {"id", "created_by"},
		"team_members":             {"team_id", "user_id"},
		"tasks":                    {"id", "created_by", "team_id", "assignee_id"},
		"task_history":             {"id", "task_id", "changed_by"},
		"task_comments":            {"id", "task_id", "user_id"},
		"team_task_cache_versions": {"team_id"},
	}
	for table, cols := range idColumns {
		for _, col := range cols {
			var typ string
			err := db.QueryRow(`SELECT column_type FROM information_schema.columns
				WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`, table, col).Scan(&typ)
			if err != nil {
				t.Fatalf("%s.%s: %v", table, col, err)
			}
			if !strings.EqualFold(typ, "binary(16)") {
				t.Errorf("%s.%s: тип %s, ожидался binary(16)", table, col, typ)
			}
		}
	}

	var versionType string
	if err := db.QueryRow(`SELECT column_type FROM information_schema.columns
		WHERE table_schema = DATABASE() AND table_name = 'tasks' AND column_name = 'version'`).Scan(&versionType); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(versionType, "bigint") {
		t.Errorf("tasks.version должна быть числовой: %s", versionType)
	}
}

func insertUser(t *testing.T, db *sql.DB, id uuid.UUID, email string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO users (id, email, password_hash, name, created_at) VALUES (?, ?, 'h', 'N', ?)`,
		platform.IDBytes(id), email, time.Now().UTC())
	if err != nil {
		t.Fatalf("insert user: %v", err)
	}
}

func TestUUIDBytesPreservedAndForeignKeys(t *testing.T) {
	db, _ := testutil.MySQL(t)
	now := time.Now().UTC()

	userID, teamID, taskID := mustID(t), mustID(t), mustID(t)
	insertUser(t, db, userID, "a@example.com")

	// Строка в БД должна совпадать с байтами RFC без перестановки частей.
	var raw []byte
	if err := db.QueryRow(`SELECT id FROM users WHERE email = 'a@example.com'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(userID[:]) {
		t.Fatalf("байты изменены при хранении: % x != % x", raw, userID[:])
	}
	back, err := platform.IDFromBytes(raw)
	if err != nil || back != userID {
		t.Fatalf("чтение UUID: %v %v", back, err)
	}
	var hexID string
	if err := db.QueryRow(`SELECT LOWER(HEX(id)) FROM users LIMIT 1`).Scan(&hexID); err != nil {
		t.Fatal(err)
	}
	if hexID != strings.ReplaceAll(userID.String(), "-", "") {
		t.Fatalf("hex в БД %s не совпадает со строкой %s", hexID, userID)
	}

	if _, err := db.Exec(`INSERT INTO teams (id, name, created_by, created_at) VALUES (?, 'T', ?, ?)`,
		platform.IDBytes(teamID), platform.IDBytes(userID), now); err != nil {
		t.Fatalf("insert team: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO team_members (team_id, user_id, role, created_at) VALUES (?, ?, 'owner', ?)`,
		platform.IDBytes(teamID), platform.IDBytes(userID), now); err != nil {
		t.Fatalf("insert member: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO team_task_cache_versions (team_id, revision) VALUES (?, 0)`,
		platform.IDBytes(teamID)); err != nil {
		t.Fatalf("insert cache version: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO tasks (id, title, description, status, created_by, team_id, version, created_at, updated_at)
		VALUES (?, 't', '', 'todo', ?, ?, 1, ?, ?)`,
		platform.IDBytes(taskID), platform.IDBytes(userID), platform.IDBytes(teamID), now, now); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_history (id, task_id, changed_by, task_version, changes, created_at)
		VALUES (?, ?, ?, 1, JSON_OBJECT('title', JSON_OBJECT('old', NULL, 'new', 't')), ?)`,
		platform.IDBytes(mustID(t)), platform.IDBytes(taskID), platform.IDBytes(userID), now); err != nil {
		t.Fatalf("insert history: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO task_comments (id, task_id, user_id, content, created_at) VALUES (?, ?, ?, 'c', ?)`,
		platform.IDBytes(mustID(t)), platform.IDBytes(taskID), platform.IDBytes(userID), now); err != nil {
		t.Fatalf("insert comment: %v", err)
	}

	var gotTask []byte
	if err := db.QueryRow(`SELECT t.id FROM tasks t JOIN teams m ON m.id = t.team_id
		JOIN users u ON u.id = t.created_by WHERE t.id = ?`, platform.IDBytes(taskID)).Scan(&gotTask); err != nil {
		t.Fatalf("join по UUID: %v", err)
	}
	if string(gotTask) != string(taskID[:]) {
		t.Fatal("байты задачи изменились")
	}

	// FK: ссылки на несуществующие строки отклоняются.
	ghost := platform.IDBytes(mustID(t))
	fkViolations := map[string]string{
		"team.created_by":  `INSERT INTO teams (id, name, created_by, created_at) VALUES (?, 'X', ?, NOW(6))`,
		"member.team":      `INSERT INTO team_members (team_id, user_id, role, created_at) VALUES (?, ?, 'member', NOW(6))`,
		"cache.version.fk": `INSERT INTO team_task_cache_versions (team_id, revision) VALUES (?, 0)`,
		"task.team":        `INSERT INTO tasks (id, title, description, status, created_by, team_id, version, created_at, updated_at) VALUES (?, 't', '', 'todo', ?, ?, 1, NOW(6), NOW(6))`,
		"history.task":     `INSERT INTO task_history (id, task_id, changed_by, task_version, changes, created_at) VALUES (?, ?, ?, 1, '{}', NOW(6))`,
		"comment.task":     `INSERT INTO task_comments (id, task_id, user_id, content, created_at) VALUES (?, ?, ?, 'c', NOW(6))`,
	}
	argsFor := map[string][]any{
		"team.created_by":  {platform.IDBytes(mustID(t)), ghost},
		"member.team":      {ghost, platform.IDBytes(userID)},
		"cache.version.fk": {ghost},
		"task.team":        {platform.IDBytes(mustID(t)), platform.IDBytes(userID), ghost},
		"history.task":     {platform.IDBytes(mustID(t)), ghost, platform.IDBytes(userID)},
		"comment.task":     {platform.IDBytes(mustID(t)), ghost, platform.IDBytes(userID)},
	}
	for name, q := range fkViolations {
		if _, err := db.Exec(q, argsFor[name]...); err == nil {
			t.Errorf("%s: ожидалось нарушение FK", name)
		}
	}
}

func TestSchemaConstraints(t *testing.T) {
	db, _ := testutil.MySQL(t)
	now := time.Now().UTC()
	userID, teamID, taskID := mustID(t), mustID(t), mustID(t)
	insertUser(t, db, userID, "dup@example.com")

	// Уникальный нормализованный email.
	if _, err := db.Exec(`INSERT INTO users (id, email, password_hash, name, created_at) VALUES (?, 'dup@example.com', 'h', 'N', ?)`,
		platform.IDBytes(mustID(t)), now); err == nil {
		t.Error("повтор email должен нарушать уникальность")
	}

	if _, err := db.Exec(`INSERT INTO teams (id, name, created_by, created_at) VALUES (?, 'T', ?, ?)`,
		platform.IDBytes(teamID), platform.IDBytes(userID), now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO team_members (team_id, user_id, role, created_at) VALUES (?, ?, 'boss', ?)`,
		platform.IDBytes(teamID), platform.IDBytes(userID), now); err == nil {
		t.Error("недопустимая роль должна отклоняться CHECK")
	}
	insertTask := func(id uuid.UUID, status string, version int) error {
		_, err := db.Exec(`INSERT INTO tasks (id, title, description, status, created_by, team_id, version, created_at, updated_at)
			VALUES (?, 't', '', ?, ?, ?, ?, ?, ?)`, platform.IDBytes(id), status, platform.IDBytes(userID),
			platform.IDBytes(teamID), version, now, now)
		return err
	}
	if err := insertTask(mustID(t), "blocked", 1); err == nil {
		t.Error("недопустимый статус должен отклоняться CHECK")
	}
	if err := insertTask(mustID(t), "todo", 0); err == nil {
		t.Error("version < 1 должна отклоняться CHECK")
	}
	if err := insertTask(taskID, "todo", 1); err != nil {
		t.Fatal(err)
	}
	insertHistory := func(version int) error {
		_, err := db.Exec(`INSERT INTO task_history (id, task_id, changed_by, task_version, changes, created_at) VALUES (?, ?, ?, ?, '{}', ?)`,
			platform.IDBytes(mustID(t)), platform.IDBytes(taskID), platform.IDBytes(userID), version, now)
		return err
	}
	if err := insertHistory(1); err != nil {
		t.Fatal(err)
	}
	if err := insertHistory(1); err == nil {
		t.Error("повтор (task_id, task_version) должен нарушать уникальность")
	}
	if _, err := db.Exec(`INSERT INTO team_task_cache_versions (team_id, revision) VALUES (?, -1)`,
		platform.IDBytes(teamID)); err == nil {
		t.Error("отрицательная ревизия должна отклоняться CHECK")
	}
}
