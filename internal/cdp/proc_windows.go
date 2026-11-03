//go:build windows

package cdp

import (
	"os/exec"
	"strconv"
	"syscall"
)

// detach is a no-op on Windows: taskkill /T follows the process tree.
func detach(cmd *exec.Cmd) {}

// killTree ends a process and its children (Edge spawns a helper process per tab).
func killTree(pid int) {
	cmd := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid))
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = cmd.Run()
}
