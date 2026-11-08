package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claude-orchestrator/internal/plugins"
)

// The profile with the Google login goes to the bundled Gemini plugin and to nothing else.
func TestOnlyTheBundledGeminiGetsItsProfile(t *testing.T) {
	data := t.TempDir()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home folder")
	}
	t.Setenv("LUCODE_AIMODE_PROFILE", "")
	want := filepath.Join(home, ".lucode", "aimode-profile")

	name, port, profile := edgeSetup(data, plugins.GeminiID, sourceBundled)
	if profile != want || port != geminiPort || name != "aimode" {
		t.Fatalf("bundled: %s %d %s", name, port, profile)
	}
	custom := filepath.Join(data, "custom")
	t.Setenv("LUCODE_AIMODE_PROFILE", custom)
	if _, _, profile := edgeSetup(data, plugins.GeminiID, sourceBundled); profile != custom {
		t.Fatalf("the variable: %s", profile)
	}
	for _, c := range []struct{ id, source string }{
		{plugins.GeminiID, "upload"}, // should a row ever say so
		{plugins.GeminiID, ""},
		{"acme.gemini", sourceBundled},
		{"acme.gemini", "upload"},
		{"lucode.gemini2", sourceBundled},
	} {
		name, port, profile := edgeSetup(data, c.id, c.source)
		if profile == want || profile == custom || port != 0 || name == "aimode" || !strings.HasPrefix(profile, data) || !strings.Contains(profile, c.id) {
			t.Errorf("%s from %q: %s %d %s", c.id, c.source, name, port, profile)
		}
	}
}
