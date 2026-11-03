package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	natspkg "claude-orchestrator/internal/natsbus"
)

// A request from a peer over NATS gets through without a token; one on the HTTP
// port still needs it, and the NATS mark cannot be faked with a header.
func TestAuthMiddlewareTrustsOnlyNATSRequests(t *testing.T) {
	h := &Handler{}
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	srv := h.authMiddleware(ok)

	plain := httptest.NewRecorder()
	srv.ServeHTTP(plain, httptest.NewRequest("GET", "/api/ai-profiles", nil))
	if plain.Code != http.StatusUnauthorized {
		t.Errorf("no token over HTTP: got %d, want 401", plain.Code)
	}

	faked := httptest.NewRequest("GET", "/api/ai-profiles", nil)
	faked.Header.Set("X-Lucode-Rpc", "true")
	faked.Header.Set("X-Lucode-Remote", "x")
	fakedRec := httptest.NewRecorder()
	srv.ServeHTTP(fakedRec, faked)
	if fakedRec.Code != http.StatusUnauthorized {
		t.Errorf("headers must not fake a NATS request: got %d", fakedRec.Code)
	}

	over := httptest.NewRequest("GET", "/api/ai-profiles", nil)
	over = over.WithContext(natspkg.WithRPC(context.Background()))
	overRec := httptest.NewRecorder()
	srv.ServeHTTP(overRec, over)
	if overRec.Code != http.StatusNoContent {
		t.Errorf("a request over NATS: got %d, want it to pass", overRec.Code)
	}
}
