package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
)

func TestDiffStat(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	repo := t.TempDir()
	rgit(t, repo, "init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "keep.txt"), "a\nb\nc\n")
	write(t, filepath.Join(repo, "gone.txt"), "x\ny\n")
	rgit(t, repo, "add", ".")
	rgit(t, repo, "commit", "-q", "-m", "base")

	wt := filepath.Join(t.TempDir(), "wt")
	rgit(t, repo, "worktree", "add", "-q", "-b", "task", wt)
	// committed: modify keep.txt (+1 -1), delete gone.txt (-2), create new.txt (+3)
	write(t, filepath.Join(wt, "keep.txt"), "a\nB\nc\n")
	if err := os.Remove(filepath.Join(wt, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(wt, "new.txt"), "1\n2\n3\n")
	rgit(t, wt, "add", ".")
	rgit(t, wt, "commit", "-q", "-m", "work")
	// uncommitted: an untracked file with 2 lines
	write(t, filepath.Join(wt, "scratch.txt"), "p\nq\n")

	rv := dbpkg.CardReview{Branch: "task", WorktreePath: wt, RepoPath: repo}
	got := diffStat(rv)
	want := DiffStat{Base: "main", Branch: "task", Added: 1 + 3 + 2, Removed: 1 + 2, Created: 2, Modified: 1, Deleted: 1}
	if got != want {
		t.Fatalf("worktree on disk:\n got %+v\nwant %+v", got, want)
	}

	// without the worktree only what is committed counts
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	rgit(t, repo, "worktree", "prune")
	got = diffStat(rv)
	want = DiffStat{Base: "main", Branch: "task", Added: 1 + 3, Removed: 1 + 2, Created: 1, Modified: 1, Deleted: 1}
	if got != want {
		t.Fatalf("worktree gone:\n got %+v\nwant %+v", got, want)
	}
}
