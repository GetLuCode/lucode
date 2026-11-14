//go:build windows

package termrun

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"

	pty "github.com/aymanbagabas/go-pty"
)

func terminalEnv() []string { return nil }

// shellCommand runs the command line with cmd.exe, which every Windows has; an empty command opens
// PowerShell (pwsh if there is one), the shell people expect to find.
func shellCommand(pt pty.Pty, chosen Shell, command string) *pty.Cmd {
	if chosen.Path != "" {
		return chosen.command(pt, command)
	}
	if command == "" {
		for _, name := range []string{"pwsh.exe", "powershell.exe"} {
			if path, err := exec.LookPath(name); err == nil {
				return pt.Command(path, "-NoLogo")
			}
		}
		return pt.Command(comspec())
	}
	return cmdExeCommand(pt, comspec(), command)
}

// cmdExeCommand runs the line with the given cmd.exe.
func cmdExeCommand(pt pty.Pty, path, command string) *pty.Cmd {
	cmd := pt.Command(path)
	// the line goes through as it is: cmd.exe does its own quoting, Go's escaping would break it
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `"` + path + `" /d /s /c "` + command + `"`}
	return cmd
}

func comspec() string {
	if c := os.Getenv("ComSpec"); c != "" {
		return c
	}
	return `C:\Windows\System32\cmd.exe`
}

// killTree ends the process and its children: closing the console alone leaves them running.
func killTree(pid int) {
	if pid <= 0 {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
