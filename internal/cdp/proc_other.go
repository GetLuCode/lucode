//go:build !windows

package cdp

import (
	"os/exec"
	"syscall"
)

// detach makes the browser its own process group so killTree reaches its helpers.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// killTree ends a process; the browser was started as its own process group leader.
func killTree(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
