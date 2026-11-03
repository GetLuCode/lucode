package aimode

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
