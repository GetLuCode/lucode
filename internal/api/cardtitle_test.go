package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGenerateCardTitle(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"content":[{"type":"text","text":"\"Fix login bug\"\n"}]}`))
	}))
	defer srv.Close()
	t.Setenv("ANTHROPIC_API_KEY", "k")
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)

	if title := generateCardTitle(context.Background(), "the login page crashes"); title != "Fix login bug" {
		t.Fatalf("title = %q", title)
	}
	if got["model"] != titleModel || got["tools"] != nil {
		t.Fatalf("bad request: %v", got)
	}
}

func TestGenerateCardTitleFallback(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if title := generateCardTitle(context.Background(), "first line\nsecond"); title != "first line" {
		t.Fatalf("title = %q", title)
	}
}
