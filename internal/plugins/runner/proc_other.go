//go:build !windows

package runner

import (
	"os/exec"
	"syscall"
)

// prepareTree gives the child its own process group, so its whole tree can be signalled.
func prepareTree(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }

// killTree stops a process and everything it started.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
