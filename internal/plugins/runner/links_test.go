package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func evalRoot(t *testing.T, dir string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// The path rules alone, with no NATS, so they can run wherever links can be made.
func TestResolvePathRefusesSymlinks(t *testing.T) {
	root := evalRoot(t, t.TempDir())
	outside := evalRoot(t, t.TempDir())
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, ".env"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "fine.txt"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(root, "real"), 0o755)
	symlinkOrSkip(t, filepath.Join(outside, "secret.txt"), filepath.Join(root, "leak.txt"))
	symlinkOrSkip(t, outside, filepath.Join(root, "leakdir"))
	symlinkOrSkip(t, filepath.Join(root, ".env"), filepath.Join(root, "notes.txt"))
	symlinkOrSkip(t, filepath.Join(root, "fine.txt"), filepath.Join(root, "alias.txt"))
	symlinkOrSkip(t, "../../..", filepath.Join(root, "up"))

	for rel, ok := range map[string]bool{
		"leak.txt": false, "leakdir/secret.txt": false, "leakdir/new/deep.txt": false, "notes.txt": false, "up/x": false,
		"alias.txt": true, "fine.txt": true, "real/new.txt": true, "new/dir/file.txt": true,
	} {
		if _, err := ResolvePath(root, rel); (err == nil) != ok {
			t.Errorf("ResolvePath(%q): %v", rel, err)
		}
	}
}

// On Windows anyone can make a junction, with no privilege at all, and a junction is not a link to
// the path functions. It leaves the workspace just the same.
func TestResolvePathRefusesJunctions(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("junctions are a Windows thing")
	}
	root := evalRoot(t, t.TempDir())
	outside := evalRoot(t, t.TempDir())
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o644)
	os.Mkdir(filepath.Join(root, "real"), 0o755)
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(root, "leakdir"), outside).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", filepath.Join(root, "real", "nested"), outside).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}
	for _, rel := range []string{"leakdir", "leakdir/secret.txt", "leakdir/new.txt", "real/nested/secret.txt"} {
		if _, err := ResolvePath(root, rel); err == nil {
			t.Errorf("ResolvePath(%q) went through a junction", rel)
		}
	}
	if _, err := ResolvePath(root, "real/new.txt"); err != nil {
		t.Errorf("a plain folder: %v", err)
	}
}
