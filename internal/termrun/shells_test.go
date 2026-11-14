package termrun

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// echoVar is the command line that prints the variable, in the syntax of that kind of shell.
func echoVar(kind, name string) string {
	switch kind {
	case kindPwsh:
		return "Write-Output ('V=' + $env:" + name + ")"
	case kindCmd:
		return "echo V=%" + name + "%"
	}
	return "echo V=$" + name
}

func TestDetectFindsAShellAndEachRunsACommandWithItsEnv(t *testing.T) {
	shells := Detect()
	if len(shells) == 0 {
		t.Skip("no shell detected on this machine")
	}
	for _, sh := range shells {
		t.Run(sh.ID, func(t *testing.T) {
			if !sh.Builtin || !filepath.IsAbs(sh.Path) {
				t.Fatalf("%+v: a detected shell is Builtin and has an absolute path", sh)
			}
			rec := &recorder{}
			m := NewManager(rec)
			line := echoVar(shellKind(sh.Path), "LUCODE_TEST_VAR")
			if _, err := m.Start(Spec{ID: "s", Command: line, Cwd: t.TempDir(), Env: []string{"LUCODE_TEST_VAR=hello-env"}, Shell: sh}); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "exit", func() bool { return rec.last().State == StateExited })
			if !strings.Contains(rec.text(), "V=hello-env") {
				t.Fatalf("output %q lacks the variable the command was started with", rec.text())
			}
		})
	}
}

func TestACommandWithoutAShellKeepsTheDefault(t *testing.T) {
	rec := &recorder{}
	m := NewManager(rec)
	line := "echo V=$LUCODE_TEST_VAR"
	if runtime.GOOS == "windows" {
		line = "echo V=%LUCODE_TEST_VAR%"
	}
	if _, err := m.Start(Spec{ID: "d", Command: line, Cwd: t.TempDir(), Env: []string{"LUCODE_TEST_VAR=plain"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "exit", func() bool { return rec.last().State == StateExited })
	if !strings.Contains(rec.text(), "V=plain") {
		t.Fatalf("output %q", rec.text())
	}
}

func TestCheckCustom(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "myshell")
	if runtime.GOOS == "windows" {
		file += ".exe"
	}
	if err := os.WriteFile(file, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	name, path, args, err := CheckCustom("  My shell ", file, []string{" -c ", "", "-x"})
	if err != nil || name != "My shell" || path != file || len(args) != 2 || args[0] != "-c" {
		t.Fatalf("got %q %q %v %v", name, path, args, err)
	}
	for what, c := range map[string]struct {
		name, path string
		args       []string
	}{
		"no name":       {"", file, nil},
		"relative path": {"x", "bin/sh", nil},
		"missing file":  {"x", filepath.Join(dir, "nope"), nil},
		"a directory":   {"x", dir, nil},
		"many args":     {"x", file, []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}},
	} {
		if _, _, _, err := CheckCustom(c.name, c.path, c.args); err == nil {
			t.Errorf("%s: accepted", what)
		}
	}
	if runtime.GOOS != "windows" {
		plain := filepath.Join(dir, "plain")
		if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := CheckCustom("x", plain, nil); err == nil {
			t.Error("a file that is not executable was accepted")
		}
	}
}

func TestShellKind(t *testing.T) {
	for path, want := range map[string]string{
		`C:\Program Files\PowerShell\7\pwsh.exe`: kindPwsh,
		"/usr/bin/pwsh":                          kindPwsh,
		`C:\Windows\System32\cmd.exe`:            kindCmd,
		"/bin/bash":                              kindPosix,
		"/usr/bin/fish":                          kindPosix,
		"/opt/odd/shell":                         kindPosix,
	} {
		// filepath.Base only splits on the separators of the OS the test runs on
		if got := shellKind(filepath.FromSlash(strings.ReplaceAll(path, `\`, "/"))); got != want {
			t.Errorf("%s: kind %q, want %q", path, got, want)
		}
	}
}
