package executor

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestWorkspaceEventReportsWorktreeAndBranch(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init", "-q", "-b", "main")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(root, "wt")
	git(t, repo, "worktree", "add", "-q", "-b", "agent/work", wt)

	main := WorkspaceEvent("sess", repo)
	if main["branch"] != "main" || main["worktree"] != false || main["type"] != "claude_workspace" {
		t.Errorf("main checkout: %v", main)
	}

	ev := WorkspaceEvent("sess", filepath.Join(wt)) // a subdirectory resolves to the same root
	if ev["branch"] != "agent/work" || ev["worktree"] != true {
		t.Errorf("worktree: %v", ev)
	}
	if got, want := filepath.Clean(ev["path"].(string)), filepath.Clean(wt); !samePath(got, want) {
		t.Errorf("path = %q, want %q", got, want)
	}
	if got, ok := ev["repo"].(string); !ok || !samePath(got, repo) {
		t.Errorf("repo = %v, want %q", ev["repo"], repo)
	}

	plain := WorkspaceEvent("sess", root)
	if _, has := plain["branch"]; has || plain["cwd"] != root {
		t.Errorf("not a git checkout: only the cwd is known: %v", plain)
	}
}

func samePath(a, b string) bool {
	ai, err1 := os.Stat(a)
	bi, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(ai, bi)
}

func TestInitEventCarriesCwd(t *testing.T) {
	ev, _, err := ParseLine("s", []byte(`{"type":"system","subtype":"init","cwd":"E:\\wt","session_id":"x"}`))
	if err != nil || ev.Cwd != `E:\wt` || ev.Status != "running" {
		t.Errorf("init: %+v %v", ev, err)
	}
}
