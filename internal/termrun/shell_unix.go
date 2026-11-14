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
func shellCommand(pt pty.Pty, chosen Shell, command string) *pty.Cmd {
	if chosen.Path != "" {
		return chosen.command(pt, command)
	}
	sh := os.Getenv("SHELL")
	if sh == "" {
		sh = "/bin/sh"
	}
	if command == "" {
		return pt.Command(sh)
	}
	return pt.Command(sh, "-c", command)
}

func comspec() string { return "" } // Windows only; shells.go names it in code that does not run here

// cmdExeCommand is only for Windows; a registered cmd.exe on another system gets the plain form.
func cmdExeCommand(pt pty.Pty, path, command string) *pty.Cmd { return pt.Command(path, "/c", command) }

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
