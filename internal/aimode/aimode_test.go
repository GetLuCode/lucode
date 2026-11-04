package aimode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCleanRemovesPageNoise(t *testing.T) {
	raw := "Un motor transforma energía.\nIFEMA Madrid\n +3\nAdmisión: baja el pistón.\n\n\n\nReferencias web:\nRACE: https://example.com/race\nLa IA puede cometer errores, así que verifica las respuestas\n"
	got := Clean(raw)
	want := "Un motor transforma energía.\nIFEMA Madrid\nAdmisión: baja el pistón.\n\nReferencias web:\nRACE: https://example.com/race"
	if got != want {
		t.Errorf("Clean:\n%q\nwant\n%q", got, want)
	}
	if got := Clean("Hola\nAI can make mistakes, so double-check responses"); got != "Hola" {
		t.Errorf("english notice: %q", got)
	}
	if got := Clean("   \n"); got != "" {
		t.Errorf("blank: %q", got)
	}
}

// A real conversation with Google's AI Mode through Edge. It needs the network and Edge,
// so it only runs when asked: LUCODE_LIVE_AIMODE=1 go test ./internal/aimode -run Live -v
func TestLiveConversation(t *testing.T) {
	if os.Getenv("LUCODE_LIVE_AIMODE") == "" {
		t.Skip("set LUCODE_LIVE_AIMODE=1 to talk to Google")
	}
	t.Setenv("LUCODE_EDGE_STATE", filepath.Join(t.TempDir(), "state.json"))
	t.Setenv("LUCODE_AIMODE_HEADLESS", "1")
	c := newClient("aimode-live", 9455, t.TempDir())
	t.Cleanup(c.Shutdown)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	a1, err := c.Ask(ctx, "s1", "¿Cuál es la capital de Francia? Responde en una sola oración.")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("answer 1: %q", a1)
	if !strings.Contains(strings.ToLower(a1), "par") {
		t.Errorf("expected Paris in %q", a1)
	}
	// the same conversation: it must remember we were talking about capitals
	a2, err := c.Ask(ctx, "s1", "¿Y la de Italia?")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("answer 2: %q", a2)
	if !strings.Contains(strings.ToLower(a2), "roma") {
		t.Errorf("expected Rome in %q", a2)
	}
	// a message with several lines
	a3, err := c.Ask(ctx, "s1", "Responde solo con el resultado:\nuno más\ndos")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("answer 3: %q", a3)
	c.Forget("s1")
}

// memStore is an in-memory Store for the tests.
type memStore struct {
	mu          sync.Mutex
	url, titled string
	turns       int
}

func (m *memStore) ConversationURL(string) (string, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.url, m.turns
}
func (m *memStore) SaveConversation(_ string, url, title string, turns int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.url, m.turns = url, turns
	if m.titled == "" {
		m.titled = title
	}
}

// The history: after the whole browser is closed, a new client with the same profile reopens
// the saved url and goes on in that same conversation (its earlier turns are still on the
// page), instead of starting a new one. What Gemini remembers is not what is checked.
// LUCODE_LIVE_AIMODE=1 go test ./internal/aimode -run Resume -v
func TestLiveResumeAfterRestart(t *testing.T) {
	if os.Getenv("LUCODE_LIVE_AIMODE") == "" {
		t.Skip("set LUCODE_LIVE_AIMODE=1 to talk to Google")
	}
	t.Setenv("LUCODE_EDGE_STATE", filepath.Join(t.TempDir(), "state.json"))
	t.Setenv("LUCODE_AIMODE_HEADLESS", "1")
	profile := t.TempDir() // kept for both clients, like ~/.lucode/aimode-profile
	store := &memStore{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	first := newClient("aimode-r1", 9456, profile)
	first.SetStore(store)
	if _, err := first.Ask(ctx, "s1", "Vamos a hablar de la ciudad de Tokio. Responde solo: listo."); err != nil {
		t.Fatal(err)
	}
	if store.url == "" || store.titled == "" {
		t.Fatalf("the conversation was not saved: %+v", store)
	}
	t.Logf("saved %q (%d chars of url)", store.titled, len(store.url))
	first.Shutdown() // browser closed: cookies are flushed

	second := newClient("aimode-r2", 9457, profile) // another lucode run: no tabs, no memory
	second.SetStore(store)
	t.Cleanup(second.Shutdown)
	a, err := second.Ask(ctx, "s1", "¿En qué país está esa ciudad? Responde solo el país.")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("answer: %q", a)
	// the page holds the primer, the first question and now this one: three turns in one thread
	if n := second.turns(); n != 3 {
		t.Errorf("expected to continue the saved conversation (3 turns), the page has %d", n)
	}
	if store.titled != "Vamos a hablar de la ciudad de Tokio. Responde solo: listo." {
		t.Errorf("the title must stay the first question, got %q", store.titled)
	}
}
