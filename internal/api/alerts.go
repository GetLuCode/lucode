package api

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	dbpkg "claude-orchestrator/internal/db"
)

// Alerts. Things the user may want to clean up by hand. lucode only reports them:
// it never deletes anything here (the merge from Review already cleans its own worktree).

// Alert is one entry of the bell dropdown.
type Alert struct {
	ID      string `json:"id"`      // stable: the same problem keeps the same id
	Kind    string `json:"kind"`    // "stale_worktree"
	Title   string `json:"title"`
	Detail  string `json:"detail"`
	Command string `json:"command"` // what the user can run to clean it up
}

var lockPidRe = regexp.MustCompile(`\(pid (\d+)\)`)

// pidAlive reports whether a process with that pid exists on this machine. A reused
// pid can say "alive" for a dead session: the alert then shows up late, never wrongly.
func pidAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		_ = p.Release()
		return true // FindProcess opens the process on Windows: it fails when it is gone
	}
	return p.Signal(syscall.Signal(0)) == nil
}

type worktreeInfo struct {
	path, branch string
	head         string
	detached     bool
	prunable     bool
	locked       bool
	lockReason   string
}

func parseWorktrees(porcelain string) []worktreeInfo {
	var out []worktreeInfo
	var cur *worktreeInfo
	flush := func() {
		if cur != nil {
			out = append(out, *cur)
			cur = nil
		}
	}
	for _, l := range strings.Split(porcelain, "\n") {
		l = strings.TrimRight(l, "\r")
		switch {
		case l == "":
			flush()
		case strings.HasPrefix(l, "worktree "):
			flush()
			cur = &worktreeInfo{path: strings.TrimPrefix(l, "worktree ")}
		case cur == nil:
		case strings.HasPrefix(l, "branch refs/heads/"):
			cur.branch = strings.TrimPrefix(l, "branch refs/heads/")
		case strings.HasPrefix(l, "HEAD "):
			cur.head = strings.TrimPrefix(l, "HEAD ")
		case l == "detached":
			cur.detached = true
		case l == "prunable" || strings.HasPrefix(l, "prunable "):
			cur.prunable = true
		case l == "locked" || strings.HasPrefix(l, "locked "):
			cur.locked = true
			cur.lockReason = strings.TrimSpace(strings.TrimPrefix(l, "locked"))
		}
	}
	flush()
	return out
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.ToSlash(filepath.Clean(a)), filepath.ToSlash(filepath.Clean(b)))
}

// staleWorktrees lists the linked worktrees of a repository whose work is already in
// the checked-out branch and that nothing is using any more. It is conservative: a
// worktree is reported only if it is clean, its branch is merged and either
//   - claude locked it for a session whose process is gone, or
//   - it is not locked and its branch was merged with a merge commit (a fresh
//     worktree with no commits yet looks "merged" too, so that alone is not enough).
//
// Worktrees waiting in Review (reviewPaths) and locks of a live process are left out.
func staleWorktrees(repo string, reviewPaths []string) []Alert {
	out, err := gitIn(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return nil
	}
	wts := parseWorktrees(out)
	if len(wts) < 2 {
		return nil
	}
	var mergeTips map[string]bool // second parents of the merge commits of HEAD, loaded on demand
	var alerts []Alert
next:
	for _, wt := range wts[1:] { // the first one is the main checkout
		if wt.branch == "" {
			continue
		}
		for _, p := range reviewPaths {
			if samePath(p, wt.path) {
				continue next
			}
		}
		if _, err := os.Stat(wt.path); err != nil {
			continue
		}
		if _, err := gitIn(repo, "merge-base", "--is-ancestor", wt.branch, "HEAD"); err != nil {
			continue // work not in HEAD yet (or git failed): not ours to call stale
		}
		if st, err := gitIn(wt.path, "status", "--porcelain"); err != nil || st != "" {
			continue
		}
		why := ""
		if wt.locked {
			m := lockPidRe.FindStringSubmatch(wt.lockReason)
			if m == nil {
				continue // locked by someone for a reason we cannot read: leave it
			}
			pid, _ := strconv.Atoi(m[1])
			if pidAlive(pid) {
				continue
			}
			why = fmt.Sprintf("its claude session (pid %d) is gone and the lock was never released", pid)
		} else {
			if mergeTips == nil {
				mergeTips = map[string]bool{}
				if revs, err := gitIn(repo, "rev-list", "--merges", "--parents", "-n", "500", "HEAD"); err == nil {
					for _, l := range strings.Split(revs, "\n") {
						if f := strings.Fields(l); len(f) >= 3 {
							mergeTips[f[2]] = true
						}
					}
				}
			}
			tip, err := gitIn(repo, "rev-parse", wt.branch)
			if err != nil || !mergeTips[tip] {
				continue
			}
			why = "its branch was merged and nobody is using it"
		}
		cmds := []string{}
		if wt.locked {
			cmds = append(cmds, fmt.Sprintf(`git -C "%s" worktree unlock "%s"`, repo, wt.path))
		}
		cmds = append(cmds,
			fmt.Sprintf(`git -C "%s" worktree remove --force "%s"`, repo, wt.path),
			fmt.Sprintf(`git -C "%s" branch -d "%s"`, repo, wt.branch))
		alerts = append(alerts, Alert{
			ID:      "stale_worktree:" + filepath.ToSlash(wt.path),
			Kind:    "stale_worktree",
			Title:   "Merged worktree left behind: " + wt.branch,
			Detail:  wt.path + ": " + why + ".",
			Command: strings.Join(cmds, " && "),
		})
	}
	return alerts
}

// scanWorktreeAlerts looks, on THIS machine, at the repositories of dirs. Git and the
// pid check only mean something where the worktrees and the claude sessions are, so
// this runs on the runner; the hub asks the runner instead of looking at its own disk.
func scanWorktreeAlerts(dirs, reviewPaths []string) []Alert {
	alerts := []Alert{}
	seen := map[string]bool{}
	for _, dir := range dirs {
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		common, err := gitIn(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil || filepath.Base(common) != ".git" {
			continue
		}
		repo := filepath.Dir(filepath.FromSlash(common))
		if seen[strings.ToLower(repo)] {
			continue
		}
		seen[strings.ToLower(repo)] = true
		alerts = append(alerts, staleWorktrees(repo, reviewPaths)...)
	}
	return alerts
}

// staleWorktreesRPC is what a runner serves at GET /api/worktrees/stale?dir=..&review=..:
// the merged worktrees left behind in the repositories of the given dirs. review lists
// the worktrees the hub has waiting in Review, which must not be reported.
func (h *Handler) staleWorktreesRPC(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	jsonResp(w, http.StatusOK, scanWorktreeAlerts(q["dir"], q["review"]))
}

// listProjectAlerts reports what needs cleaning up for the project. The runner the
// project works on does the looking (its repositories are on its disk); the hub only
// relays and shows. Only a project with no remote runner is scanned in this process.
func (h *Handler) listProjectAlerts(w http.ResponseWriter, r *http.Request) {
	proj, err := dbpkg.GetProject(h.db, r.PathValue("pid"))
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	dirs := h.projectWorkDirs(proj)
	review := dbpkg.CardReviewWorktreePaths(h.db)
	if !h.isRemoteRunner(proj.RunnerID) {
		jsonResp(w, http.StatusOK, scanWorktreeAlerts(dirs, review))
		return
	}
	q := url.Values{"dir": dirs, "review": review}
	var alerts []Alert
	code, err := h.runnerCall(r.Context(), proj.RunnerID, http.MethodGet, "/api/worktrees/stale?"+q.Encode(), nil, &alerts)
	if code == http.StatusNotFound {
		// a runner built before this endpoint: nothing to show, not a failure
		jsonResp(w, http.StatusOK, []Alert{})
		return
	}
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}
	if alerts == nil {
		alerts = []Alert{}
	}
	jsonResp(w, http.StatusOK, alerts)
}
