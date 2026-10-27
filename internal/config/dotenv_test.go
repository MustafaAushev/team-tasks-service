package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDotEnv(t *testing.T) {
	vars, err := parseDotEnv(strings.NewReader(`
# комментарий
MYSQL_HOST=127.0.0.1
export MYSQL_PORT = 3307
PLAIN=a b # хвост
HASH=pa#ss
EMPTY=
B64=ab+/cd==
SINGLE='lit\n "x" # не комментарий'
DOUBLE="line\nnext \"q\" \\ #" # комментарий
`))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"MYSQL_HOST": "127.0.0.1",
		"MYSQL_PORT": "3307",
		"PLAIN":      "a b",
		"HASH":       "pa#ss",
		"EMPTY":      "",
		"B64":        "ab+/cd==",
		"SINGLE":     `lit\n "x" # не комментарий`,
		"DOUBLE":     "line\nnext \"q\" \\ #",
	}
	if len(vars) != len(want) {
		t.Fatalf("переменные: %q", vars)
	}
	for k, v := range want {
		if vars[k] != v {
			t.Errorf("%s = %q, ожидалось %q", k, vars[k], v)
		}
	}
}

func TestParseDotEnvErrors(t *testing.T) {
	for name, src := range map[string]string{
		"нет =":               "JWT_SECRET",
		"плохой ключ":         "1KEY=v",
		"незакрытая \"":       `JWT_SECRET="secret-value`,
		"незакрытая '":        `JWT_SECRET='secret-value`,
		"хвост после кавычек": `JWT_SECRET="a" secret-value`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := parseDotEnv(strings.NewReader("OK=1\n" + src))
			if err == nil || !strings.Contains(err.Error(), "строка 2") {
				t.Fatalf("ошибка: %v", err)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("значение попало в ошибку: %v", err)
			}
		})
	}
}

// Переменная из команды запуска приоритетнее файла, даже пустая; без файла остаётся окружение.
func TestGetenvPriority(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("FROM_FILE=file\nOVERRIDDEN=file\nBLANKED=file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OVERRIDDEN", "process")
	t.Setenv("BLANKED", "")
	t.Setenv("ONLY_PROCESS", "process")

	getenv, err := Getenv(path)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"FROM_FILE": "file", "OVERRIDDEN": "process", "BLANKED": "", "ONLY_PROCESS": "process", "MISSING": "",
	} {
		if got := getenv(k); got != want {
			t.Errorf("%s = %q, ожидалось %q", k, got, want)
		}
	}

	getenv, err = Getenv(filepath.Join(t.TempDir(), ".env"))
	if err != nil || getenv("ONLY_PROCESS") != "process" || getenv("FROM_FILE") != "" {
		t.Fatalf("без файла: err=%v", err)
	}

	if err := os.WriteFile(path, []byte("broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Getenv(path); err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("битый файл: %v", err)
	}
	if _, err := Getenv(t.TempDir()); err == nil {
		t.Fatal("каталог вместо файла должен давать ошибку")
	}
}
