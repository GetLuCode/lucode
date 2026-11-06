package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	dbpkg "claude-orchestrator/internal/db"

	"github.com/google/uuid"
)

// Review. An agent that worked in a git worktree does not finish into Done: its
// task goes to the Review list with the branch it left. From there the user either
// merges it (when git says it merges cleanly) or sends the task back to the agent
// to resolve the conflicts. Git runs on the machine of the hub, so the repository
// has to be reachable from it; a worktree on a remote runner can only be reviewed
// by hand.

// gitIn runs git in dir and returns stdout; on failure the error carries stderr.
func gitIn(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return strings.TrimSpace(out.String()), errors.New(msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// resolveReview finds the worktree a task session worked in: the cwd its claude
// process reported (--worktree) or else the worktree its transcript shows it created.
func (h *Handler) resolveReview(sessID, cardID string) (dbpkg.CardReview, bool) {
	runner := h.sessionRunner(sessID)
	if h.manager != nil {
		if ws := h.manager.WorkspaceOf(sessID); ws != nil {
			path, _ := ws["path"].(string)
			branch, _ := ws["branch"].(string)
			repo, _ := ws["repo"].(string)
			if wt, _ := ws["worktree"].(bool); wt && path != "" && branch != "" && repo != "" {
				return dbpkg.CardReview{CardID: cardID, Branch: branch, WorktreePath: path, RepoPath: repo, RunnerID: runner}, true
			}
		}
	}
	path, branch := dbpkg.SessionWorktree(h.db, sessID)
	if path == "" || branch == "" || !filepath.IsAbs(path) {
		return dbpkg.CardReview{}, false
	}
	// which repository the worktree belongs to is a fact of the disk it is on
	repo, ok := h.repoOn(runner, path)
	if !ok {
		return dbpkg.CardReview{}, false
	}
	return dbpkg.CardReview{CardID: cardID, Branch: branch, WorktreePath: path, RepoPath: repo, RunnerID: runner}, true
}

// ReviewStatus is what the review dialog shows about a task's branch.
type ReviewStatus struct {
	Branch        string   `json:"branch"`
	Base          string   `json:"base"`          // the branch checked out in the main checkout: where it would be merged
	WorktreePath  string   `json:"worktree_path"`
	RepoPath      string   `json:"repo_path"`
	Ahead         int      `json:"ahead"`         // commits on the branch that are not in base
	Files         []string `json:"files"`         // "M path" per file the branch changes
	Dirty         bool     `json:"dirty"`         // uncommitted changes in the worktree
	Conflicts     bool     `json:"conflicts"`     // merging would conflict
	ConflictFiles []string `json:"conflict_files"`
	Merged        bool     `json:"merged"`        // nothing left to merge
	Error         string   `json:"error,omitempty"`
}

func reviewStatus(rv dbpkg.CardReview) ReviewStatus {
	st := ReviewStatus{Branch: rv.Branch, WorktreePath: rv.WorktreePath, RepoPath: rv.RepoPath, Files: []string{}, ConflictFiles: []string{}}
	if _, err := os.Stat(rv.RepoPath); err != nil {
		st.Error = "the repository is not reachable from this machine (" + rv.RepoPath + "): merge it by hand where the agent ran"
		return st
	}
	base, err := gitIn(rv.RepoPath, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil || base == "HEAD" {
		st.Error = "the main checkout is not on a branch, so there is nothing to merge into"
		return st
	}
	st.Base = base
	if _, err := gitIn(rv.RepoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+rv.Branch); err != nil {
		st.Merged = true // the branch is gone: it was merged and cleaned up by hand
		return st
	}
	if n, err := gitIn(rv.RepoPath, "rev-list", "--count", base+".."+rv.Branch); err == nil {
		st.Ahead, _ = strconv.Atoi(n)
	}
	if _, err := os.Stat(rv.WorktreePath); err == nil {
		if out, err := gitIn(rv.WorktreePath, "status", "--porcelain"); err == nil && out != "" {
			st.Dirty = true
		}
	}
	st.Merged = st.Ahead == 0 && !st.Dirty
	if out, err := gitIn(rv.RepoPath, "diff", "--name-status", base+"..."+rv.Branch); err == nil && out != "" {
		for _, l := range strings.Split(out, "\n") {
			st.Files = append(st.Files, strings.Join(strings.Fields(l), " "))
		}
	}
	if st.Ahead > 0 {
		// merge-tree merges in memory: exit 1 = conflicts, listing the files after the tree id
		out, err := gitIn(rv.RepoPath, "merge-tree", "--write-tree", "--name-only", base, rv.Branch)
		if err != nil {
			st.Conflicts = true
			lines := strings.Split(out, "\n")
			for _, l := range lines[min(1, len(lines)):] {
				l = strings.TrimSpace(l)
				if l == "" {
					break
				}
				st.ConflictFiles = append(st.ConflictFiles, l)
			}
			if len(st.ConflictFiles) == 0 {
				st.Error = err.Error()
			}
		}
	}
	return st
}

// reviewFor returns the saved review of a card in the Review list.
func (h *Handler) reviewFor(w http.ResponseWriter, r *http.Request) (dbpkg.KanbanCard, dbpkg.CardReview, bool) {
	card, err := dbpkg.GetKanbanCard(h.db, r.PathValue("id"))
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return card, dbpkg.CardReview{}, false
	}
	rv, err := dbpkg.GetCardReview(h.db, card.ID)
	if err != nil {
		http.Error(w, "this task has no branch waiting for review", http.StatusNotFound)
		return card, rv, false
	}
	return card, rv, true
}

func (h *Handler) getCardReview(w http.ResponseWriter, r *http.Request) {
	_, rv, ok := h.reviewFor(w, r)
	if !ok {
		return
	}
	jsonResp(w, http.StatusOK, h.reviewStatusOf(rv))
}

// mergeCardReview merges the task's branch into the main checkout's branch, removes
// the worktree and branch, and finishes the task. It refuses when git sees conflicts
// or the worktree has uncommitted work.
func (h *Handler) mergeCardReview(w http.ResponseWriter, r *http.Request) {
	card, rv, ok := h.reviewFor(w, r)
	if !ok {
		return
	}
	// the runner that holds the repository does the git work; the hub keeps the state
	res, code, err := h.mergeOnRunner(rv, card.Title)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	moved, err := h.finishMerge(card, rv, res, true, dbpkg.EventCause{Source: "user"})
	if err != nil {
		http.Error(w, "merged, but could not move the task to Done: "+err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"card": moved, "warnings": res.Warnings})
}

// resolveCardReview sends the task back to its agent, in the same session, to
// commit what is missing or merge the base branch into its own and fix the conflicts.
func (h *Handler) resolveCardReview(w http.ResponseWriter, r *http.Request) {
	card, rv, ok := h.reviewFor(w, r)
	if !ok {
		return
	}
	st := h.reviewStatusOf(rv)
	var note string
	switch {
	case st.Conflicts:
		files := "the files git reports"
		if len(st.ConflictFiles) > 0 {
			files = strings.Join(st.ConflictFiles, ", ")
		}
		note = fmt.Sprintf("Your work on branch %s cannot be merged into %s: it conflicts in %s. In your worktree run `git merge %s`, resolve the conflicts keeping the intent of both sides, run the project's checks, commit the merge and call task_done again. Do not merge into %s yourself or push.",
			rv.Branch, st.Base, files, st.Base, st.Base)
	case st.Dirty:
		note = fmt.Sprintf("Your worktree for branch %s has uncommitted changes. Commit them and call task_done again.", rv.Branch)
	default:
		note = "The user sent this task back for another pass. Look at the latest comments, finish what is missing and call task_done again."
	}
	_ = dbpkg.InsertCardComment(h.db, dbpkg.CardComment{
		ID: uuid.New().String(), CardID: card.ID, Author: "lucode", Kind: "comment", Body: note,
	})
	moved, err := h.moveCardToKind(card, dbpkg.KanbanInProgress)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if moved.AgentID == "" {
		http.Error(w, "the task has no agent to send it to", http.StatusConflict)
		return
	}
	if err := h.runTaskNote(moved, note); err != nil {
		log.Printf("[review] send %q back: %v", moved.Title, err)
		_ = dbpkg.InsertCardComment(h.db, dbpkg.CardComment{
			ID: uuid.New().String(), CardID: card.ID, Author: "lucode", Kind: "error", Body: err.Error(),
		})
		if blocked, mErr := h.moveCardToKind(moved, dbpkg.KanbanBlocked); mErr == nil {
			moved = blocked
		}
	}
	jsonResp(w, http.StatusOK, map[string]any{"card": moved})
}

