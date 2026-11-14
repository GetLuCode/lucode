package termrun

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	pty "github.com/aymanbagabas/go-pty"
)

// Shell is a shell a command can run in. The ones a runner finds on its own are Builtin; the user can
// register more (the runner keeps them, see db.TermShell). A Spec without a shell uses the default of
// the system (shellCommand in shell_<os>.go).
type Shell struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Args    []string `json:"args"`    // put before the command line; empty = the usual ones for that kind of shell
	Builtin bool     `json:"builtin"` // found on the runner, not registered by the user

	interactive []string // arguments when no command is given (the shell opens interactively)
}

const (
	kindPosix = "posix" // bash, zsh, sh...: -c "line"
	kindPwsh  = "pwsh"  // pwsh and Windows PowerShell: -Command "line"
	kindCmd   = "cmd"   // cmd.exe: /d /s /c "line", which needs its own quoting
)

// shellKind tells how to hand a command line to a shell, by the name of its executable.
func shellKind(path string) string {
	base := strings.ToLower(filepath.Base(path))
	base = strings.TrimSuffix(base, ".exe")
	switch base {
	case "pwsh", "powershell":
		return kindPwsh
	case "cmd":
		return kindCmd
	}
	return kindPosix
}

// commandArgs are the arguments that go before the command line.
func (s Shell) commandArgs() []string {
	if len(s.Args) > 0 {
		return s.Args
	}
	switch shellKind(s.Path) {
	case kindPwsh:
		return []string{"-NoLogo", "-Command"}
	case kindCmd:
		return []string{"/d", "/s", "/c"}
	}
	return []string{"-c"}
}

// command builds the process: the shell opens interactively for an empty line, else runs the line.
func (s Shell) command(pt pty.Pty, line string) *pty.Cmd {
	if line == "" {
		in := s.interactive
		if in == nil && shellKind(s.Path) == kindPwsh {
			in = []string{"-NoLogo"}
		}
		return pt.Command(s.Path, in...)
	}
	if shellKind(s.Path) == kindCmd && len(s.Args) == 0 {
		return cmdExeCommand(pt, s.Path, line)
	}
	return pt.Command(s.Path, append(append([]string{}, s.commandArgs()...), line)...)
}

func idOf(path string) string {
	base := strings.ToLower(filepath.Base(path))
	return strings.TrimSuffix(base, ".exe")
}

// Detect lists the shells this runner has, the ones it can run without anyone registering them.
func Detect() []Shell {
	var out []Shell
	seenPath := map[string]bool{}
	seenID := map[string]bool{}
	add := func(id, name, path string, interactive []string) {
		path = filepath.Clean(path)
		key := strings.ToLower(path)
		if seenPath[key] || seenID[id] {
			return
		}
		if fi, err := os.Stat(path); err != nil || fi.IsDir() {
			return
		}
		seenPath[key], seenID[id] = true, true
		out = append(out, Shell{ID: id, Name: name, Path: path, Builtin: true, interactive: interactive})
	}
	if runtime.GOOS == "windows" {
		for _, n := range []struct{ id, name, exe string }{{"pwsh", "PowerShell 7 (pwsh)", "pwsh.exe"}, {"powershell", "Windows PowerShell", "powershell.exe"}} {
			if p, err := exec.LookPath(n.exe); err == nil {
				add(n.id, n.name, p, nil)
			}
		}
		add("cmd", "Command Prompt (cmd)", comspec(), nil)
		// Git Bash by its install folders: "bash.exe" on the PATH is often the WSL launcher, a different thing
		for _, root := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), filepath.Join(os.Getenv("LocalAppData"), "Programs")} {
			if root != "" && root != "Programs" {
				add("gitbash", "Git Bash", filepath.Join(root, "Git", "bin", "bash.exe"), []string{"-i", "-l"})
			}
		}
		return out
	}
	for _, name := range []string{"bash", "zsh", "fish", "sh", "dash", "ksh"} {
		if p, err := exec.LookPath(name); err == nil {
			add(name, name, p, nil)
		}
	}
	// the shells the system lists as valid login shells
	if f, err := os.Open("/etc/shells"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(line, "/") {
				add(idOf(line), idOf(line), line, nil)
			}
		}
	}
	return out
}

// Find resolves a shell id among the detected ones; the caller looks at the registered ones.
func Find(id string) (Shell, bool) {
	for _, s := range Detect() {
		if s.ID == id {
			return s, true
		}
	}
	return Shell{}, false
}

const (
	maxShellName = 60
	maxShellArgs = 8
	maxShellArg  = 200
)

// CheckCustom validates what the user registers: a name, an absolute path to an executable file that
// exists on this runner, and a few plain arguments. Nothing here is run.
func CheckCustom(name, path string, args []string) (string, string, []string, error) {
	name, path = strings.TrimSpace(name), strings.TrimSpace(path)
	if name == "" || len(name) > maxShellName {
		return "", "", nil, fmt.Errorf("give the shell a name (up to %d characters)", maxShellName)
	}
	if !filepath.IsAbs(path) {
		return "", "", nil, errors.New("the path of the shell must be absolute, as it is on the runner")
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "", "", nil, fmt.Errorf("that file does not exist on this runner: %s", path)
	}
	if fi.IsDir() || (runtime.GOOS != "windows" && fi.Mode()&0o111 == 0) {
		return "", "", nil, fmt.Errorf("that is not an executable file: %s", path)
	}
	clean := make([]string, 0, len(args))
	for _, a := range args {
		if a = strings.TrimSpace(a); a != "" {
			if len(a) > maxShellArg || strings.ContainsRune(a, 0) {
				return "", "", nil, errors.New("an argument of the shell is too long")
			}
			clean = append(clean, a)
		}
	}
	if len(clean) > maxShellArgs {
		return "", "", nil, fmt.Errorf("too many arguments (max %d)", maxShellArgs)
	}
	return name, path, clean, nil
}
