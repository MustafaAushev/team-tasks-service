//go:build api

package api

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/secunda/team-tasks-service/internal/testutil"
)

// TestDependencyProbe запускается только дочерним процессом: он пробует получить MySQL/Redis так же,
// как любой тест набора.
func TestDependencyProbe(t *testing.T) {
	if os.Getenv("DEPENDENCY_PROBE") != "1" {
		t.Skip("служебный тест: запускается из TestMissingDependencyFailsLoudly")
	}
	testutil.StartAPI(t, testutil.APIOptions{})
}

// Набор, которому нужны MySQL/Redis, обязан упасть, а не молча пропустить проверки.
func TestMissingDependencyFailsLoudly(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"переменные не заданы", map[string]string{"TEST_MYSQL_ADMIN_DSN": ""}, "тестовая зависимость не настроена"},
		{"MySQL недоступен", map[string]string{"TEST_MYSQL_ADMIN_DSN": "root:x@tcp(127.0.0.1:1)/"}, "недоступен"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestDependencyProbe$", "-test.v")
			cmd.Env = append(os.Environ(), "DEPENDENCY_PROBE=1")
			for k, v := range c.env {
				cmd.Env = append(cmd.Env, k+"="+v)
			}
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("дочерний набор должен был завершиться ошибкой:\n%s", out)
			}
			if strings.Contains(string(out), "--- SKIP") || strings.Contains(string(out), "--- PASS") {
				t.Fatalf("проверка не должна пропускаться или проходить:\n%s", out)
			}
			if !strings.Contains(string(out), c.want) {
				t.Fatalf("в выводе нет %q:\n%s", c.want, out)
			}
		})
	}
}
