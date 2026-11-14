//go:build !windows

package termrun

import (
	"os"
	"syscall"
	"time"

	pty "github.com/aymanbagabas/go-pty"
)

func terminalEnv() []string { return []string{"TERM=xterm-256color", "COLORTERM=truecolor"} }

// shellCommand runs the command line with the user's shell (sh without one); an empty command
// opens that shell interactively.
func shellCommand(pt pty.Pty, command string) *pty.Cmd {
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	if command == "" {
		return pt.Command(sh)
	}
	return pt.Command(sh, "-c", command)
}

// killTree ends the process group (the pty makes the shell its leader): a polite signal first,
// then SIGKILL if something is still there after three seconds.
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	go func() {
		time.Sleep(3 * time.Second)
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}()
}
