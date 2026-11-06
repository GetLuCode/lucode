// Package version says which build of lucode is running: the git commit it was
// built from (Go records it in the binary), whether the tree had uncommitted
// changes, and when the binary was written.
package version

import (
	"os"
	"runtime"
	"runtime/debug"
	"time"
)

// Info is what GET /api/version answers on a hub and on a runner.
type Info struct {
	Commit     string    `json:"commit,omitempty"`      // full git hash the binary was built from
	Short      string    `json:"short"`                 // 8 chars of it + "+dirty" with uncommitted changes; "unknown" if not recorded
	CommitTime string    `json:"commit_time,omitempty"` // RFC3339, when that commit was made
	Dirty      bool      `json:"dirty"`
	BuiltAt    time.Time `json:"built_at,omitzero"` // modification time of the executable
	StartedAt  time.Time `json:"started_at"`
	Go         string    `json:"go"`
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
	Role       string    `json:"role,omitempty"` // "runner" | "hub"
}

var started = time.Now()

// Current reads the build information of this process.
func Current(role string) Info {
	info := Info{Short: "unknown", StartedAt: started, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, Role: role}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				info.Commit = s.Value
			case "vcs.time":
				info.CommitTime = s.Value
			case "vcs.modified":
				info.Dirty = s.Value == "true"
			}
		}
	}
	if info.Commit != "" {
		info.Short = info.Commit
		if len(info.Short) > 8 {
			info.Short = info.Short[:8]
		}
		if info.Dirty {
			info.Short += "+dirty"
		}
	}
	if exe, err := os.Executable(); err == nil {
		if st, err := os.Stat(exe); err == nil {
			info.BuiltAt = st.ModTime()
		}
	}
	return info
}
