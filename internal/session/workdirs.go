package session

import (
	"os"
	"path/filepath"
)

// ResolveWorkDirs makes the project's work dirs absolute on the machine that runs
// claude: a relative one ("lucode", ".\lucode") is taken relative to the instance's
// working dir (--working-dir, or the user's home without it), the same root the
// folder picker browses. Absolute dirs are left as they are.
func (m *Manager) ResolveWorkDirs(dirs []string) []string {
	if len(dirs) == 0 {
		return dirs
	}
	base := m.BaseDir
	if base == "" {
		base, _ = os.UserHomeDir()
	}
	out := make([]string, len(dirs))
	for i, d := range dirs {
		if d == "" || filepath.IsAbs(d) || base == "" {
			out[i] = d
			continue
		}
		out[i] = filepath.Join(base, d)
	}
	return out
}
