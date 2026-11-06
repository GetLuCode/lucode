package session

import (
	"path/filepath"
	"testing"
)

func TestResolveWorkDirsRelativeToBaseDir(t *testing.T) {
	base := t.TempDir()
	abs := filepath.Join(t.TempDir(), "repo")
	m := &Manager{BaseDir: base}
	got := m.ResolveWorkDirs([]string{"lucode", filepath.Join(".", "sub", "x"), abs})
	want := []string{filepath.Join(base, "lucode"), filepath.Join(base, "sub", "x"), abs}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dir %d = %q, want %q", i, got[i], want[i])
		}
	}
	if got := m.ResolveWorkDirs(nil); len(got) != 0 {
		t.Errorf("nil: %v", got)
	}
}
