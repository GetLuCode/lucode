//go:build !windows

package plugins

import "os"

// restrictToOwner takes everyone else off a file.
func restrictToOwner(file string) error { return os.Chmod(file, 0o600) }
