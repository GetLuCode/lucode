package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	dbpkg "claude-orchestrator/internal/db"
)

// Review, split between the two processes. The repository and the worktree of a task
// are on the disk of the RUNNER that ran the agent, so the runner is the one that
// runs git on them (status, merge, cleanup) and serves it over the RPC bridge:
//
//	POST /api/review/status   body reviewRef            -> ReviewStatus
//	POST /api/review/merge    body mergeRequest         -> mergeResult (or an error text + status)
//	POST /api/review/merge-options body mergeOptionsRequest -> the same, with a Merge node's choices
//	POST /api/review/repo     body {"path": "<dir>"}    -> {"repo": "<main checkout>"}
//
// The hub only keeps the state (the card, its Review row, the comments, the closed
// chat) and calls these. A review whose runner is this very instance (RunnerID "") is
// answered in-process: that is a legacy row, not a place new code should run git.

type reviewRef struct {
	RepoPath     string `json:"repo_path"`
	Branch       string `json:"branch"`
	WorktreePath string `json:"worktree_path"`
}

func (r reviewRef) review() dbpkg.CardReview {
	return dbpkg.CardReview{Branch: r.Branch, WorktreePath: r.WorktreePath, RepoPath: r.RepoPath}
}

func refOf(rv dbpkg.CardReview) reviewRef {
	return reviewRef{RepoPath: rv.RepoPath, Branch: rv.Branch, WorktreePath: rv.WorktreePath}
}

type mergeRequest struct {
	reviewRef
	Title string `json:"title"`
}

type mergeResult struct {
	Base            string   `json:"base"`             // the branch it was merged into
	Warnings        []string `json:"warnings"`         // cleanup that did not work
	WorktreeRemoved bool     `json:"worktree_removed"` // the agent's directory is gone
}

// mergeOptions are what a Merge node can choose. The zero value is what the Review button does.
// It has its own route so a runner that predates it answers 404, instead of merging and
// ignoring the options, which would delete a worktree someone asked to keep.
type mergeOptions struct {
	KeepWorktree bool   `json:"keep_worktree,omitempty"`
	KeepBranch   bool   `json:"keep_branch,omitempty"`
	Strategy     string `json:"strategy,omitempty"` // "" (a merge commit), "squash" or "ff-only"
	Message      string `json:"message,omitempty"`  // "" = Merge task: <title>
}

type mergeOptionsRequest struct {
	reviewRef
	Title   string       `json:"title"`
	Options mergeOptions `json:"options"`
}

// mergeReviewBranch merges the task's branch into the main checkout's branch and
// removes the worktree and the branch. It refuses (409) when git sees conflicts or the
// worktree has uncommitted work. It runs where the repository is.
func mergeReviewBranch(rv dbpkg.CardReview, title string) (mergeResult, int, error) {
	return mergeWithOptions(rv, title, mergeOptions{})
}

// runMerge does the git merge itself. Anything it refuses leaves the checkout as it was.
func runMerge(rv dbpkg.CardReview, message, strategy string) error {
	switch strategy {
	case "":
		if _, err := gitIn(rv.RepoPath, "merge", "--no-ff", "-m", message, rv.Branch); err != nil {
			_, _ = gitIn(rv.RepoPath, "merge", "--abort")
			return errors.New("git merge failed: " + err.Error())
		}
	case "squash":
		if _, err := gitIn(rv.RepoPath, "merge", "--squash", rv.Branch); err != nil {
			_, _ = gitIn(rv.RepoPath, "reset", "--merge") // a squash leaves no merge to abort
			return errors.New("git merge --squash failed: " + err.Error())
		}
		if _, err := gitIn(rv.RepoPath, "-c", "user.useConfigOnly=false", "commit", "-m", message); err != nil {
			_, _ = gitIn(rv.RepoPath, "reset", "--merge")
			return errors.New("could not commit the squash: " + err.Error())
		}
	case "ff-only":
		if _, err := gitIn(rv.RepoPath, "merge", "--ff-only", rv.Branch); err != nil {
			return errors.New("cannot fast-forward: the base branch moved. " + err.Error())
		}
	default:
		return errors.New("unknown merge strategy " + strategy)
	}
	return nil
}

func mergeWithOptions(rv dbpkg.CardReview, title string, opts mergeOptions) (mergeResult, int, error) {
	st := reviewStatus(rv)
	switch {
	case st.Error != "":
		return mergeResult{}, http.StatusConflict, errors.New(st.Error)
	case st.Conflicts:
		return mergeResult{}, http.StatusConflict, errors.New("the branch conflicts with " + st.Base + ": send it to the agent to resolve")
	case st.Dirty:
		return mergeResult{}, http.StatusConflict, errors.New("the worktree has uncommitted changes: send it to the agent to commit them")
	}
	res := mergeResult{Base: st.Base, Warnings: []string{}}
	if st.Ahead > 0 {
		message := opts.Message
		if strings.TrimSpace(message) == "" {
			message = "Merge task: " + title
		}
		if err := runMerge(rv, message, opts.Strategy); err != nil {
			return mergeResult{}, http.StatusConflict, err
		}
	}
	if _, err := os.Stat(rv.WorktreePath); err == nil && !opts.KeepWorktree {
		// claude locks the worktrees of its sessions and never unlocks them, and git
		// will not remove a locked worktree with a single --force. The work is merged
		// by now, so the lock has nothing left to protect. Not being locked is an
		// error we ignore.
		_, _ = gitIn(rv.RepoPath, "worktree", "unlock", rv.WorktreePath)
		if _, err := gitIn(rv.RepoPath, "worktree", "remove", "--force", rv.WorktreePath); err != nil {
			res.Warnings = append(res.Warnings, "could not remove the worktree: "+err.Error())
		}
	}
	if _, err := os.Stat(rv.WorktreePath); os.IsNotExist(err) {
		res.WorktreeRemoved = true
	}
	if !opts.KeepBranch {
		// after a squash git cannot see the branch as merged, though its work is in
		deleteFlag := "-d"
		if opts.Strategy == "squash" {
			deleteFlag = "-D"
		}
		if _, err := gitIn(rv.RepoPath, "branch", deleteFlag, rv.Branch); err != nil {
			res.Warnings = append(res.Warnings, "could not delete the branch: "+err.Error())
		}
	}
	return res, http.StatusOK, nil
}

// repoOfWorktree is the main checkout a worktree belongs to.
func repoOfWorktree(path string) (string, bool) {
	common, err := gitIn(path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || filepath.Base(common) != ".git" {
		return "", false
	}
	return filepath.Dir(filepath.FromSlash(common)), true
}

// ---- runner side ----

func (h *Handler) reviewStatusRPC(w http.ResponseWriter, r *http.Request) {
	var ref reviewRef
	if err := json.NewDecoder(r.Body).Decode(&ref); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonResp(w, http.StatusOK, reviewStatus(ref.review()))
}

func (h *Handler) reviewMergeRPC(w http.ResponseWriter, r *http.Request) {
	var req mergeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, code, err := mergeReviewBranch(req.review(), req.Title)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	jsonResp(w, http.StatusOK, res)
}

func (h *Handler) reviewMergeOptionsRPC(w http.ResponseWriter, r *http.Request) {
	var req mergeOptionsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, code, err := mergeWithOptions(req.review(), req.Title, req.Options)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	jsonResp(w, http.StatusOK, res)
}

func (h *Handler) reviewRepoRPC(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	repo, ok := repoOfWorktree(body.Path)
	if !ok {
		http.Error(w, "not a git worktree", http.StatusNotFound)
		return
	}
	jsonResp(w, http.StatusOK, map[string]string{"repo": repo})
}

// ---- hub side ----

// runnerCall sends a JSON request to a runner over the RPC bridge and decodes a 200
// answer into out. Any other answer comes back as its status and text.
func (h *Handler) runnerCall(ctx context.Context, runnerID, method, path string, in, out any) (int, error) {
	var body io.Reader = http.NoBody
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return http.StatusInternalServerError, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := h.bus.RPCRequest(runnerID, req)
	if err != nil {
		return http.StatusBadGateway, fmt.Errorf("runner %s unavailable: %w", runnerID, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, errors.New(strings.TrimSpace(string(msg)))
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return http.StatusBadGateway, fmt.Errorf("bad answer from runner %s: %w", runnerID, err)
		}
	}
	return http.StatusOK, nil
}

// sessionRunner is the runner that ran the session's claude ("" = this instance).
func (h *Handler) sessionRunner(sessID string) string {
	c, err := dbpkg.LatestClaudeCommand(h.db, sessID)
	if err != nil || !h.isRemoteRunner(c.RunnerID) {
		return ""
	}
	return c.RunnerID
}

func (h *Handler) reviewStatusOf(rv dbpkg.CardReview) ReviewStatus {
	if !h.isRemoteRunner(rv.RunnerID) {
		return reviewStatus(rv)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var st ReviewStatus
	if _, err := h.runnerCall(ctx, rv.RunnerID, http.MethodPost, "/api/review/status", refOf(rv), &st); err != nil {
		return ReviewStatus{Branch: rv.Branch, WorktreePath: rv.WorktreePath, RepoPath: rv.RepoPath,
			Files: []string{}, ConflictFiles: []string{}, Error: err.Error()}
	}
	return st
}

func (h *Handler) mergeOnRunner(rv dbpkg.CardReview, title string) (mergeResult, int, error) {
	if !h.isRemoteRunner(rv.RunnerID) {
		return mergeReviewBranch(rv, title)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	var res mergeResult
	if code, err := h.runnerCall(ctx, rv.RunnerID, http.MethodPost, "/api/review/merge", mergeRequest{refOf(rv), title}, &res); err != nil {
		return mergeResult{}, code, err
	}
	return res, http.StatusOK, nil
}

// repoOn finds the main checkout of a worktree on the disk of the given runner.
func (h *Handler) repoOn(runnerID, path string) (string, bool) {
	if !h.isRemoteRunner(runnerID) {
		return repoOfWorktree(path)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var out struct {
		Repo string `json:"repo"`
	}
	if _, err := h.runnerCall(ctx, runnerID, http.MethodPost, "/api/review/repo", map[string]string{"path": path}, &out); err != nil || out.Repo == "" {
		return "", false
	}
	return out.Repo, true
}
