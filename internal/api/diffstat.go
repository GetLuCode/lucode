package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	dbpkg "claude-orchestrator/internal/db"
)

// DiffStat is what a task's branch changes against its base: lines added and removed
// and the files created, modified and deleted. It is computed by the RUNNER (git runs on
// the disk where the worktree is) and served at POST /api/review/diffstat; the hub only
// resolves which worktree a session used and forwards the call.
type DiffStat struct {
	Base     string `json:"base"`
	Branch   string `json:"branch"`
	Added    int    `json:"added"`    // lines added
	Removed  int    `json:"removed"`  // lines removed
	Created  int    `json:"created"`  // files created
	Modified int    `json:"modified"` // files modified (or renamed)
	Deleted  int    `json:"deleted"`  // files deleted
	Error    string `json:"error,omitempty"`
}

// diffStat compares the whole branch against the base: everything committed since the
// branch left the base, plus what is still uncommitted in the worktree (untracked files
// count as created).
func diffStat(rv dbpkg.CardReview) DiffStat {
	st := DiffStat{Branch: rv.Branch}
	if _, err := os.Stat(rv.RepoPath); err != nil {
		st.Error = "the repository is not reachable from this machine (" + rv.RepoPath + ")"
		return st
	}
	base, err := gitIn(rv.RepoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || base == "HEAD" {
		st.Error = "the main checkout is not on a branch"
		return st
	}
	st.Base = base
	if _, err := gitIn(rv.RepoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+rv.Branch); err != nil {
		st.Error = "the branch no longer exists (merged or removed)"
		return st
	}
	mb, err := gitIn(rv.RepoPath, "merge-base", base, rv.Branch)
	if err != nil {
		st.Error = err.Error()
		return st
	}

	// With the worktree on disk, compare its working tree (committed + uncommitted tracked
	// changes) with the merge base; without it, the committed branch only.
	dir, target := rv.RepoPath, []string{mb, rv.Branch}
	_, wtErr := os.Stat(rv.WorktreePath)
	hasWT := rv.WorktreePath != "" && wtErr == nil
	if hasWT {
		dir, target = rv.WorktreePath, []string{mb}
	}

	if out, err := gitIn(dir, append([]string{"diff", "--numstat"}, target...)...); err == nil {
		st.Added, st.Removed = sumNumstat(out)
	} else {
		st.Error = err.Error()
		return st
	}
	if out, err := gitIn(dir, append([]string{"diff", "--name-status"}, target...)...); err == nil {
		for _, l := range strings.Split(out, "\n") {
			if l == "" {
				continue
			}
			switch l[0] {
			case 'A':
				st.Created++
			case 'D':
				st.Deleted++
			default:
				st.Modified++
			}
		}
	}
	if hasWT {
		// files git does not track yet are new files of the task too
		if out, err := gitIn(dir, "ls-files", "--others", "--exclude-standard"); err == nil && out != "" {
			for _, f := range strings.Split(out, "\n") {
				st.Created++
				st.Added += countLines(filepath.Join(dir, filepath.FromSlash(f)))
			}
		}
	}
	return st
}

// sumNumstat adds up the "added<TAB>removed<TAB>path" lines of git diff --numstat
// (binary files show "-" and count as zero lines).
func sumNumstat(out string) (added, removed int) {
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(l, "\t")
		if len(f) < 3 {
			continue
		}
		a, _ := strconv.Atoi(f[0])
		r, _ := strconv.Atoi(f[1])
		added += a
		removed += r
	}
	return
}

// countLines is the number of lines of a small text file (0 for big or binary ones).
func countLines(path string) int {
	fi, err := os.Stat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > 1<<20 {
		return 0
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.IndexByte(string(b), 0) >= 0 {
		return 0
	}
	n := strings.Count(string(b), "\n")
	if len(b) > 0 && b[len(b)-1] != '\n' {
		n++
	}
	return n
}

// ---- runner side ----

func (h *Handler) diffStatRPC(w http.ResponseWriter, r *http.Request) {
	var ref reviewRef
	if err := json.NewDecoder(r.Body).Decode(&ref); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonResp(w, http.StatusOK, diffStat(ref.review()))
}

// ---- hub side ----

// diffStatOf asks the runner that holds the worktree for the stat. A review whose runner
// is this very instance is the legacy in-process case, as for the rest of the review.
func (h *Handler) diffStatOf(rv dbpkg.CardReview) DiffStat {
	if !h.isRemoteRunner(rv.RunnerID) {
		return diffStat(rv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var st DiffStat
	if _, err := h.runnerCall(ctx, rv.RunnerID, http.MethodPost, "/api/review/diffstat", refOf(rv), &st); err != nil {
		return DiffStat{Branch: rv.Branch, Error: err.Error()}
	}
	return st
}

// getSessionDiffStat serves GET /api/sessions/{id}/diffstat for the Running cards.
func (h *Handler) getSessionDiffStat(w http.ResponseWriter, r *http.Request) {
	sessID := r.PathValue("id")
	rv, ok := h.resolveReview(sessID, "")
	if !ok {
		http.Error(w, "this session has no worktree branch", http.StatusNotFound)
		return
	}
	jsonResp(w, http.StatusOK, h.diffStatOf(rv))
}
