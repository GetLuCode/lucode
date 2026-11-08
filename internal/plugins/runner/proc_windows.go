//go:build windows

package runner

import (
	"os/exec"
	"strconv"
)

func prepareTree(*exec.Cmd) {}

// killTree stops a process and everything it started.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
	_ = cmd.Process.Kill()
}
