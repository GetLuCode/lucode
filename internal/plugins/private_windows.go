//go:build windows

package plugins

import (
	"fmt"
	"os"
	"os/exec"
)

// restrictToOwner takes everyone else off a file: Windows ignores the mode bits.
func restrictToOwner(file string) error {
	user := os.Getenv("USERNAME")
	if user == "" {
		return fmt.Errorf("no user name")
	}
	if out, err := exec.Command("icacls", file, "/inheritance:r", "/grant:r", user+":F").CombinedOutput(); err != nil {
		return fmt.Errorf("icacls: %v: %s", err, out)
	}
	return nil
}
