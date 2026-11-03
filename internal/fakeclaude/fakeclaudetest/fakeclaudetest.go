// Package fakeclaudetest lets a Go test run lucode's code against a recorded
// claude session instead of the real CLI.
//
// In the package's TestMain call Intercept first, then Use inside a test:
//
//	func TestMain(m *testing.M) { fakeclaudetest.Intercept(); os.Exit(m.Run()) }
//
//	fakeclaudetest.Use(t, "two_turns.jsonl")
//
// The test binary itself plays the claude CLI when lucode spawns it.
package fakeclaudetest

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"claude-orchestrator/internal/fakeclaude"
)

// Intercept turns this process into the fake claude when lucode spawned it as one.
// Call it at the very start of TestMain.
func Intercept() {
	if os.Getenv(fakeclaude.EnvMarker) == "1" {
		fakeclaude.Main()
		os.Exit(0)
	}
}

// Use points lucode's claude lookup at this test binary, replaying the fixture of
// internal/fakeclaude/testdata with the given file name.
func Use(t testing.TB, fixture string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, here, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(here), "..", "testdata", fixture)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("fixture %s: %v", fixture, err)
	}
	t.Setenv("LUCODE_CLAUDE_BIN", exe)
	t.Setenv(fakeclaude.EnvMarker, "1")
	t.Setenv(fakeclaude.EnvFixture, path)
}
