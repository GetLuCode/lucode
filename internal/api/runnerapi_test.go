package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	natspkg "claude-orchestrator/internal/natsbus"
)

// A runner-only instance answers what a hub asks of a machine and nothing else of
// lucode's API.
func TestRunnerHandlerServesOnlyWhatAHubNeeds(t *testing.T) {
	h, _, _ := newTaskTestHandler(t)
	srv := NewRunnerHandler(h.db, natspkg.NewBus(nil), h.manager, t.TempDir())

	get := func(method, path string) int {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec.Code
	}
	for _, path := range []string{"/api/health", "/api/ai-profiles", "/api/fs/dirs", "/api/worktrees/stale"} {
		if code := get("GET", path); code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, code)
		}
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/projects"},
		{"POST", "/api/projects"},
		{"GET", "/api/agents"},
		{"POST", "/api/sessions/x/turn"},
		{"GET", "/api/runners"},
		{"GET", "/"},
	} {
		if code := get(c.method, c.path); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, a runner-only instance must not serve it", c.method, c.path, code)
		}
	}
}
