package api

import (
	"path/filepath"
	"testing"
)

func TestExpandConfigDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("LUCODE_TEST_DIR", "work")

	cases := map[string]string{
		`%USERPROFILE%\.claude-x`:   filepath.Join(home, ".claude-x"),
		`$HOME/.claude-x`:           filepath.Join(home, ".claude-x"),
		`${HOME}/.claude-x`:         filepath.Join(home, ".claude-x"),
		`~/.claude-x`:               filepath.Join(home, ".claude-x"),
		`  ` + home + `  `:          filepath.Clean(home),
		`%LUCODE_TEST_DIR%`:         "work",
		`%LUCODE_NOPE_VAR%\.claude`: filepath.Clean(`%LUCODE_NOPE_VAR%\.claude`), // unknown: left as typed
		``:                          "",
	}
	for in, want := range cases {
		if got := expandConfigDir(in); got != want {
			t.Errorf("expandConfigDir(%q) = %q, want %q", in, got, want)
		}
	}
}
