//go:build api

package api

import (
	"testing"

	"github.com/secunda/team-tasks-service/internal/testutil"
)

// Liveness не зависит от зависимостей; readiness требует MySQL со схемой и честно отвечает 503,
// когда база недоступна. Независимость readiness от Redis — в TestRateLimitWithoutRedis.
func TestHealthWithoutMySQL(t *testing.T) {
	s := testutil.StartAPI(t, testutil.APIOptions{})
	expect(t, s.Do(testutil.Request{Method: "GET", Path: "/health/ready"}), 200, "")
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	expect(t, s.Do(testutil.Request{Method: "GET", Path: "/health/live"}), 200, "")
	expect(t, s.Do(testutil.Request{Method: "GET", Path: "/health/ready"}), 503, "dependency_unavailable")
}
