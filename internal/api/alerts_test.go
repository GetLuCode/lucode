package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mergedWorktree makes reviewRepo's branch carry a commit and merges it with a merge commit.
func mergedWorktree(t *testing.T) (repo, wt string) {
	t.Helper()
	repo, wt = reviewRepo(t)
	write(t, filepath.Join(wt, "b.txt"), "new\n")
	rgit(t, wt, "add", ".")
	rgit(t, wt, "commit", "-q", "-m", "add b")
	rgit(t, repo, "merge", "-q", "--no-ff", "-m", "Merge task", "task/x")
	return repo, wt
}

func TestStaleWorktreeAlerts(t *testing.T) {
	// merged, unlocked, clean: reported, with the command to clean it
	repo, wt := mergedWorktree(t)
	alerts := staleWorktrees(repo, nil)
	if len(alerts) != 1 || alerts[0].Kind != "stale_worktree" || !strings.Contains(alerts[0].Title, "task/x") {
		t.Fatalf("alerts = %+v", alerts)
	}
	if strings.Contains(alerts[0].Command, "unlock") || !strings.Contains(alerts[0].Command, "branch -d") {
		t.Errorf("command = %s", alerts[0].Command)
	}
	// nothing was touched: reporting never cleans
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("the worktree was removed: %v", err)
	}

	// waiting in Review: not reported
	if got := staleWorktrees(repo, []string{wt}); len(got) != 0 {
		t.Errorf("a worktree in Review was reported: %+v", got)
	}

	// uncommitted work: not reported
	write(t, filepath.Join(wt, "c.txt"), "wip\n")
	if got := staleWorktrees(repo, nil); len(got) != 0 {
		t.Errorf("a dirty worktree was reported: %+v", got)
	}
}

func TestStaleWorktreeLocks(t *testing.T) {
	repo, wt := mergedWorktree(t)

	// locked by a live session: left alone
	rgit(t, repo, "worktree", "lock", "--reason", fmt.Sprintf("claude session x (pid %d)", os.Getpid()), wt)
	if got := staleWorktrees(repo, nil); len(got) != 0 {
		t.Errorf("a worktree of a live session was reported: %+v", got)
	}

	// locked by a session that is gone: reported, and the command unlocks first
	rgit(t, repo, "worktree", "unlock", wt)
	rgit(t, repo, "worktree", "lock", "--reason", "claude session x (pid 99999999)", wt)
	got := staleWorktrees(repo, nil)
	if len(got) != 1 || !strings.Contains(got[0].Command, "worktree unlock") {
		t.Fatalf("alerts = %+v", got)
	}

	// locked for a reason that has no pid: not ours to judge
	rgit(t, repo, "worktree", "unlock", wt)
	rgit(t, repo, "worktree", "lock", "--reason", "keep", wt)
	if got := staleWorktrees(repo, nil); len(got) != 0 {
		t.Errorf("a manual lock was reported: %+v", got)
	}
}

// The runner answers for its own disk: given a dir inside the repo it finds the
// repository and reports the stale worktree, skipping the ones the hub has in Review.
func TestRunnerReportsStaleWorktrees(t *testing.T) {
	repo, wt := mergedWorktree(t)
	h, _, _ := newTaskTestHandler(t)

	ask := func(query string) []Alert {
		rec := httptest.NewRecorder()
		h.staleWorktreesRPC(rec, httptest.NewRequest("GET", "/api/worktrees/stale?"+query, nil))
		var out []Alert
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return out
	}
	// two dirs of the same repo count once
	q := url.Values{"dir": {repo, wt, filepath.Join(repo, "nope")}}
	if got := ask(q.Encode()); len(got) != 1 {
		t.Fatalf("alerts = %+v", got)
	}
	q.Set("review", wt)
	if got := ask(q.Encode()); len(got) != 0 {
		t.Errorf("a worktree in Review was reported: %+v", got)
	}
	if got := ask(""); len(got) != 0 {
		t.Errorf("no dirs = %+v", got)
	}
}

func TestFreshWorktreeIsNotStale(t *testing.T) {
	// a new worktree with no commits looks merged (its tip is in HEAD): it may belong to a running agent
	repo, _ := reviewRepo(t)
	if got := staleWorktrees(repo, nil); len(got) != 0 {
		t.Errorf("a fresh worktree was reported: %+v", got)
	}
}
