package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupGitRepo creates a temp dir, inits a git repo, makes a commit, and
// returns the workDir. Skips the test if git is not installed.
func setupGitRepo(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "wt-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Best-effort: prune any worktree the test may have leaked.
		exec.Command("git", "worktree", "prune").Run()
		os.RemoveAll(dir)
	})

	cmds := [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	}
	for _, args := range cmds {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git not available or failed: %s: %v", out, err)
		}
	}

	// Create a file and commit.
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main\n"), 0644)

	for _, args := range [][]string{
		{"add", "-A"},
		{"commit", "-q", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s: %v", args, out, err)
		}
	}
	return dir
}

func TestManager_DetectsGitRepo(t *testing.T) {
	dir := setupGitRepo(t)
	m := NewManager(dir, "session-1")
	if !m.IsGitRepo() {
		t.Fatal("expected git repo to be detected")
	}
	if m.Active() {
		t.Error("Active() should be false before Ensure")
	}
}

func TestManager_PassthroughForNonGit(t *testing.T) {
	dir, _ := os.MkdirTemp("", "wt-test")
	t.Cleanup(func() { os.RemoveAll(dir) })

	m := NewManager(dir, "session-1")
	if m.IsGitRepo() {
		t.Error("non-git dir should not be detected as git")
	}
	path, err := m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if path != dir {
		t.Errorf("passthrough Ensure returned %q, want %q", path, dir)
	}
	if m.Active() {
		t.Error("non-git manager should never be Active")
	}
	// Cleanup is a no-op
	if err := m.Cleanup(); err != nil {
		t.Errorf("Cleanup should be no-op for non-git: %v", err)
	}
}

func TestManager_LazyEnsure(t *testing.T) {
	dir := setupGitRepo(t)
	m := NewManager(dir, "session-1")

	// Before Ensure, Path returns userWorkDir.
	if p := m.Path(); p != dir {
		t.Errorf("pre-Ensure Path = %q, want %q", p, dir)
	}

	// Ensure creates the worktree.
	worktree, err := m.Ensure()
	if err != nil {
		t.Fatal(err)
	}
	if !m.Active() {
		t.Error("Active should be true after Ensure")
	}
	if worktree == dir {
		t.Error("worktree path should differ from userWorkDir")
	}

	// Second Ensure returns the same path.
	worktree2, _ := m.Ensure()
	if worktree != worktree2 {
		t.Errorf("second Ensure returned %q, want %q", worktree2, worktree)
	}

	// Worktree is at the expected location.
	expected := filepath.Join(dir, ".lucode", "worktree", "session-1")
	if worktree != expected {
		t.Errorf("worktree path = %q, want %q", worktree, expected)
	}

	// Cleanup removes the worktree and the branch.
	if err := m.Cleanup(); err != nil {
		t.Errorf("Cleanup: %v", err)
	}
	if m.Active() {
		t.Error("Active should be false after Cleanup")
	}

	// Worktree dir is gone.
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("worktree dir still exists: %v", err)
	}
}

func TestManager_CleanupIdempotent(t *testing.T) {
	dir := setupGitRepo(t)
	m := NewManager(dir, "session-1")
	_, _ = m.Ensure()

	if err := m.Cleanup(); err != nil {
		t.Fatal(err)
	}
	// Second Cleanup is a no-op (no error).
	if err := m.Cleanup(); err != nil {
		t.Errorf("second Cleanup returned error: %v", err)
	}
}

func TestApply_NoChanges(t *testing.T) {
	dir := setupGitRepo(t)
	m := NewManager(dir, "session-1")
	_, _ = m.Ensure()
	t.Cleanup(func() { m.Cleanup() })

	// No edits in worktree.
	res, err := m.Apply()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.CleanFiles) != 0 || len(res.ConflictedFiles) != 0 {
		t.Errorf("expected no changes, got %+v", res)
	}
}

func TestApply_CleanTakeModel(t *testing.T) {
	dir := setupGitRepo(t)
	m := NewManager(dir, "session-1")
	worktree, _ := m.Ensure()
	t.Cleanup(func() { m.Cleanup() })

	// Model edits a.txt in the worktree.
	os.WriteFile(filepath.Join(worktree, "a.txt"), []byte("hello-updated\n"), 0644)
	// User did NOT touch a.txt. Apply should take the model's version.

	res, err := m.Apply()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ConflictedFiles) != 0 {
		t.Errorf("expected no conflicts, got %v", res.ConflictedFiles)
	}
	if len(res.CleanFiles) != 1 || res.CleanFiles[0] != "a.txt" {
		t.Errorf("expected a.txt in CleanFiles, got %v", res.CleanFiles)
	}

	// User's file is now the model's version.
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if string(got) != "hello-updated\n" {
		t.Errorf("user's a.txt = %q, want %q", got, "hello-updated\n")
	}
}

func TestApply_ConflictMarkers(t *testing.T) {
	dir := setupGitRepo(t)
	m := NewManager(dir, "session-1")
	worktree, _ := m.Ensure()
	t.Cleanup(func() { m.Cleanup() })

	// HEAD has: foo = "common\n"
	os.WriteFile(filepath.Join(dir, "foo.txt"), []byte("common\n"), 0644)
	exec.Command("git", "add", "foo.txt").Run()
	exec.Command("git", "commit", "-q", "-m", "add foo").Run()

	// Model changes: foo = "model\n"
	os.WriteFile(filepath.Join(worktree, "foo.txt"), []byte("model\n"), 0644)
	// User changes: foo = "user\n"
	os.WriteFile(filepath.Join(dir, "foo.txt"), []byte("user\n"), 0644)

	res, err := m.Apply()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ConflictedFiles) != 1 || res.ConflictedFiles[0] != "foo.txt" {
		t.Errorf("expected conflict on foo.txt, got %+v", res)
	}

	// File has markers.
	got, _ := os.ReadFile(filepath.Join(dir, "foo.txt"))
	if !containsConflictMarker(got) {
		t.Errorf("expected conflict markers, got:\n%s", got)
	}
	if !strings.Contains(string(got), "user") || !strings.Contains(string(got), "model") {
		t.Errorf("expected both 'user' and 'model' in merged file, got:\n%s", got)
	}
}

func TestApply_DeleteByModel(t *testing.T) {
	dir := setupGitRepo(t)
	// HEAD has: a.txt
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0644)
	exec.Command("git", "add", "a.txt").Run()
	exec.Command("git", "commit", "-q", "-m", "add a").Run()

	m := NewManager(dir, "session-1")
	worktree, _ := m.Ensure()
	t.Cleanup(func() { m.Cleanup() })

	// Model deletes a.txt in the worktree.
	os.Remove(filepath.Join(worktree, "a.txt"))

	res, err := m.Apply()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.DeletedFiles) != 1 || res.DeletedFiles[0] != "a.txt" {
		t.Errorf("expected a.txt in DeletedFiles, got %+v", res)
	}
	// a.txt is gone from user dir.
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Error("a.txt should be deleted from user dir")
	}
}

func TestApply_DeleteByModel_UserKeeps(t *testing.T) {
	dir := setupGitRepo(t)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0644)
	exec.Command("git", "add", "a.txt").Run()
	exec.Command("git", "commit", "-q", "-m", "add a").Run()

	m := NewManager(dir, "session-1")
	worktree, _ := m.Ensure()
	t.Cleanup(func() { m.Cleanup() })

	// User modifies a.txt (uncommitted).
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("user-kept\n"), 0644)
	// Model deletes a.txt.
	os.Remove(filepath.Join(worktree, "a.txt"))

	res, err := m.Apply()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ConflictedFiles) != 1 {
		t.Errorf("expected conflict on user-modified-deleted-by-model, got %+v", res)
	}
	// File has markers, the user's version is preserved.
	got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
	if !containsConflictMarker(got) {
		t.Errorf("expected conflict markers, got:\n%s", got)
	}
	if !strings.Contains(string(got), "user-kept") {
		t.Errorf("user's version should be in markers, got:\n%s", got)
	}
}

func TestApply_AddByModel(t *testing.T) {
	dir := setupGitRepo(t)
	m := NewManager(dir, "session-1")
	worktree, _ := m.Ensure()
	t.Cleanup(func() { m.Cleanup() })

	// Model adds new.txt in worktree.
	os.WriteFile(filepath.Join(worktree, "new.txt"), []byte("new content\n"), 0644)

	res, err := m.Apply()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.CleanFiles) != 1 || res.CleanFiles[0] != "new.txt" {
		t.Errorf("expected new.txt in CleanFiles, got %+v", res)
	}
	// new.txt now exists in user dir.
	got, _ := os.ReadFile(filepath.Join(dir, "new.txt"))
	if string(got) != "new content\n" {
		t.Errorf("user dir new.txt = %q", got)
	}
}

func TestSanitizeBranchName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"simple", "simple"},
		{"with space", "with-space"},
		// ":" "?" "*" each get replaced with "-".
		{"weird:chars?*", "weird-chars--"},
		// "..hidden.." → strings.ReplaceAll replaces both ".." with "-",
		// keeping the bit between ("hidden"). Then TrimLeft strips the
		// leading "-" (which came from the first ".."). The trailing
		// "." also gets stripped because the second ".." became "-" and
		// nothing is between it and the original end. End result: "hidden-".
		// (The behavior is fine — the produced name is still a valid
		// branch name — just a bit aggressive on dots.)
		{"..hidden..", "hidden-"},
		// Slashes are valid in git branch names (create hierarchy); we
		// only strip LEADING slashes/dashes.
		{"with/slash", "with/slash"},
		{"", "anon"},
		{"/leading-slash", "leading-slash"},
	}
	for _, tc := range tests {
		got := sanitizeBranchName(tc.in)
		if got != tc.want {
			t.Errorf("sanitizeBranchName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
