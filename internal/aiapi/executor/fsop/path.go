package fsop

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ResolvePath resolves a user-supplied path against workDir and rejects
// anything that escapes it. Empty workDir or empty input defaults to workDir
// itself. Returns a clean, absolute path on success.
//
// Cross-platform path detection: filepath.IsAbs on Windows doesn't recognize
// "/etc/passwd" as absolute (no drive letter), so we'd silently join it to
// workDir. isAbsolutePath bridges that OS-blind-spot.
func ResolvePath(p, workDir string) (string, error) {
	if workDir == "" {
		return "", fmt.Errorf("no working directory set; cannot resolve path %q", p)
	}
	workDirClean := filepath.Clean(workDir)

	var resolved string
	switch {
	case p == "":
		resolved = workDirClean
	case isAbsolutePath(p):
		// Treat as absolute even if it doesn't match the current OS's
		// expected format (e.g. "/etc/passwd" on Windows would otherwise
		// be silently joined to workDir).
		resolved = filepath.Clean(p)
	default:
		resolved = filepath.Clean(filepath.Join(workDirClean, p))
	}

	// filepath.Rel returns a relative path; if it starts with ".." or is
	// itself absolute (e.g. on Windows, a path on a different drive), the
	// caller is trying to escape workDir.
	rel, err := filepath.Rel(workDirClean, resolved)
	if err != nil {
		return "", fmt.Errorf("path %q is outside working directory: %v", p, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q is outside working directory %q", p, workDirClean)
	}
	return resolved, nil
}

// isAbsolutePath returns true if p is absolute under either the current OS's
// rules OR the "other" platform's. Prevents the OS-specific blind spot
// where filepath.IsAbs("/etc/passwd") is false on Windows (no drive letter)
// and filepath.IsAbs("C:\\foo") is false on Linux.
func isAbsolutePath(p string) bool {
	if filepath.IsAbs(p) {
		return true
	}
	if len(p) == 0 {
		return false
	}
	// Treat any leading slash (forward or backward) as absolute. Real
	// relative paths never start with a separator.
	if p[0] == '/' || p[0] == filepath.Separator {
		return true
	}
	return false
}
