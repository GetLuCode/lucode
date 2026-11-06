package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	dbpkg "claude-orchestrator/internal/db"
)

// The Git tab, hub side. The hub never runs git: it checks the directory belongs to the project,
// sends the call to the runner that holds it (gitclient.go) and adds the one thing only the hub
// knows, which task a branch or a worktree belongs to.

// gitRepoTarget is one repository the tab can open.
type gitRepoTarget struct {
	RunnerID string `json:"runner_id"`
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Online   bool   `json:"online"`
	Repo     string `json:"repo"`
}

type gitTargets struct {
	Targets []gitRepoTarget `json:"targets"`
	Reason  string          `json:"reason,omitempty"` // why there are none
}

// projectGitTargets lists the directories of the project on each runner, the project's own runner
// first. Nothing without a runner: the hub has no business with a disk.
func (h *Handler) projectGitTargets(p dbpkg.Project) gitTargets {
	res := gitTargets{Targets: []gitRepoTarget{}}
	peers := map[string][2]string{}
	for _, peer := range h.bus.Peers() {
		peers[peer.ID] = [2]string{peer.Hostname, peer.OS}
	}
	add := func(runner string) {
		if !h.isRemoteRunner(runner) {
			return
		}
		info, online := peers[runner]
		for _, dir := range p.EnvDirs[runner] {
			res.Targets = append(res.Targets, gitRepoTarget{RunnerID: runner, Hostname: info[0], OS: info[1], Online: online, Repo: dir})
		}
	}
	add(p.RunnerID)
	for runner := range p.EnvDirs {
		if runner != p.RunnerID {
			add(runner)
		}
	}
	if len(res.Targets) == 0 {
		if h.isRemoteRunner(p.RunnerID) {
			res.Reason = "This project has no work directory on its runner. Set one in the project's Environment."
		} else {
			res.Reason = "This project has no runner, and only a runner can look at a repository. Choose one in the project's Environment."
		}
	}
	return res
}

func (h *Handler) registerGit(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects/{pid}/git/targets", func(w http.ResponseWriter, r *http.Request) {
		p, err := dbpkg.GetProject(h.db, r.PathValue("pid"))
		if err != nil {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
		jsonResp(w, http.StatusOK, h.projectGitTargets(p))
	})
	mux.HandleFunc("POST /api/projects/{pid}/git/{op}", h.projectGitCall)
}

var gitOps = map[string]bool{"overview": true, "log": true, "commit": true, "diff": true, "compare": true, "stash": true, "worktrees": true, "action": true}

func (h *Handler) projectGitCall(w http.ResponseWriter, r *http.Request) {
	op := r.PathValue("op")
	if !gitOps[op] {
		http.Error(w, "unknown git operation", http.StatusNotFound)
		return
	}
	p, err := dbpkg.GetProject(h.db, r.PathValue("pid"))
	if err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var t struct {
		RunnerID string `json:"runner_id"`
		Repo     string `json:"repo"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// only the project's own directories: a browser must not be able to read any path of a runner
	allowed := false
	for _, target := range h.projectGitTargets(p).Targets {
		allowed = allowed || (target.RunnerID == t.RunnerID && target.Repo == t.Repo)
	}
	if !allowed {
		http.Error(w, "that directory is not part of this project on that runner", http.StatusForbidden)
		return
	}
	timeout := 90 * time.Second
	if op == "action" {
		timeout = gitNetTimeout + 15*time.Second // fetch, pull and push wait for the network
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	var answer json.RawMessage
	code, err := h.runnerCall(ctx, t.RunnerID, http.MethodPost, "/api/git/"+op, json.RawMessage(raw), &answer)
	if err != nil {
		if code == http.StatusNotFound {
			code = http.StatusConflict
			err = fmt.Errorf("runner %s is older than the Git tab: rebuild and restart it", t.RunnerID)
		}
		http.Error(w, err.Error(), code)
		return
	}
	if op == "worktrees" {
		var wts GitWorktrees
		if err := json.Unmarshal(answer, &wts); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		linkWorktreeTasks(&wts, h.projectTaskLinks(p.ID, t.RunnerID))
		jsonResp(w, http.StatusOK, wts)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(answer)
}

const gitDiffToolMax = 60 * 1024

// gitDiffTool answers the git_diff tool: the hub checks the directory is the project's and asks
// the runner that holds it, wherever the caller's own claude runs. Runner and repo default to the
// project's first target. Going through the same check as the tab means a tool call cannot read
// any other path of a runner.
func (h *Handler) gitDiffTool(projectID string, args json.RawMessage) (string, error) {
	p, err := dbpkg.GetProject(h.db, projectID)
	if err != nil {
		return "", fmt.Errorf("project not found: %w", err)
	}
	var req struct {
		gitDiffRequest
		RunnerID string `json:"runner_id"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return "", fmt.Errorf("bad arguments: %w", err)
	}
	targets := h.projectGitTargets(p)
	var target *gitRepoTarget
	for i := range targets.Targets {
		t := &targets.Targets[i]
		if (req.RunnerID == "" || req.RunnerID == t.RunnerID) && (req.Repo == "" || req.Repo == t.Repo) {
			target = t
			break
		}
	}
	if target == nil {
		if targets.Reason != "" {
			return "", fmt.Errorf("no repository: %s", targets.Reason)
		}
		return "", fmt.Errorf("that directory is not part of this project on that runner")
	}
	req.Repo = target.Repo
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var out GitDiff
	code, err := h.runnerCall(ctx, target.RunnerID, http.MethodPost, "/api/git/diff", req.gitDiffRequest, &out)
	if err != nil {
		if code == http.StatusNotFound {
			return "", fmt.Errorf("runner %s is older than this tool: it has to be rebuilt and restarted", target.RunnerID)
		}
		return "", err
	}
	text := out.Text
	cut := out.Truncated
	if len(text) > gitDiffToolMax {
		text, cut = text[:strings.LastIndexByte(text[:gitDiffToolMax], '\n')+1], true
	}
	if strings.TrimSpace(text) == "" {
		return "No differences.", nil
	}
	if cut {
		text += "\n[The diff is longer than this and was cut here. Ask for one path to see the rest.]\n"
	}
	return text, nil
}

// gitTaskLink ties a branch or a worktree to the task that worked on it.
type gitTaskLink struct {
	CardID    string `json:"card_id"`
	Title     string `json:"title"`
	List      string `json:"list"`
	ListKind  string `json:"list_kind"`
	Agent     string `json:"agent,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Running   bool   `json:"running,omitempty"` // its agent is working right now
	Branch    string `json:"branch"`
	Worktree  string `json:"worktree,omitempty"`
	// What lucode recorded when it merged the task. The commit comes from the runner.
	MergedInto string       `json:"merged_into,omitempty"`
	MergedAt   string       `json:"merged_at,omitempty"`
	Merge      *GitMergeRef `json:"merge,omitempty"`
}

var mergedCommentRe = regexp.MustCompile(`^Merged (\S+) into (.+)\.$`)

// projectTaskLinks finds which tasks of the project worked in a branch on that runner: from their
// transcript, what their process reported, the row waiting in Review and the note a merge leaves.
func (h *Handler) projectTaskLinks(pid, runner string) []gitTaskLink {
	sessions, err := dbpkg.ListSessions(h.db, pid)
	if err != nil {
		return nil
	}
	cards, _ := dbpkg.ListKanbanCards(h.db, pid, true)
	byID := make(map[string]dbpkg.KanbanCard, len(cards))
	for _, c := range cards {
		byID[c.ID] = c
	}
	lists, _ := dbpkg.ListKanbanLists(h.db, pid)
	listByID := make(map[string]dbpkg.KanbanList, len(lists))
	for _, l := range lists {
		listByID[l.ID] = l
	}
	agents, _ := dbpkg.ListAgents(h.db, pid)
	agentName := make(map[string]string, len(agents))
	for _, a := range agents {
		agentName[a.ID] = a.Name
	}

	var links []gitTaskLink
	seen := map[string]bool{}
	for _, s := range sessions { // newest first: a task's latest session speaks for it
		card, ok := byID[s.CardID]
		if !ok || s.CardID == "" || seen[s.CardID] {
			continue
		}
		// a session that ran on another runner worked on another disk
		if c, err := dbpkg.LatestClaudeCommand(h.db, s.ID); err == nil && c.RunnerID != "" && c.RunnerID != runner {
			continue
		}
		link := gitTaskLink{CardID: card.ID, Title: card.Title, SessionID: s.ID, Agent: agentName[card.AgentID],
			Running: s.Status == "running", List: listByID[card.ListID].Name, ListKind: listByID[card.ListID].Kind}
		link.Worktree, link.Branch = dbpkg.SessionWorktree(h.db, s.ID)
		if link.Branch == "" && h.manager != nil {
			if ws := h.manager.WorkspaceOf(s.ID); ws != nil {
				if wt, _ := ws["worktree"].(bool); wt {
					link.Worktree, _ = ws["path"].(string)
					link.Branch, _ = ws["branch"].(string)
				}
			}
		}
		if link.Branch == "" {
			if rv, err := dbpkg.GetCardReview(h.db, card.ID); err == nil {
				link.Worktree, link.Branch = rv.WorktreePath, rv.Branch
			}
		}
		if comments, err := dbpkg.ListCardComments(h.db, card.ID); err == nil {
			for _, c := range comments {
				if m := mergedCommentRe.FindStringSubmatch(c.Body); m != nil && c.Author == "lucode" {
					if link.Branch == "" {
						link.Branch = m[1]
					}
					link.MergedInto, link.MergedAt = m[2], c.CreatedAt.UTC().Format(time.RFC3339)
				}
			}
		}
		if link.Branch == "" {
			continue // this task never had a branch of its own
		}
		seen[s.CardID] = true
		links = append(links, link)
	}
	return links
}

// linkWorktreeTasks puts each task on its worktree, and the ones whose worktree is gone in the list
// of merged tasks, with the commit that took them in when its message can be recognised.
func linkWorktreeTasks(wts *GitWorktrees, links []gitTaskLink) {
	used := make([]bool, len(links))
	for i := range wts.Worktrees {
		wt := &wts.Worktrees[i]
		if wt.Main {
			continue
		}
		for j := range links {
			byPath := links[j].Worktree != "" && samePath(links[j].Worktree, wt.Path)
			byBranch := wt.Branch != "" && links[j].Branch == wt.Branch
			if !used[j] && (byPath || byBranch) {
				used[j] = true
				task := links[j]
				if wt.Merge == nil {
					wt.Merge = mergeOfTask(task, wts)
				}
				wt.Task = &task
				break
			}
		}
	}
	wts.MergedTasks = []gitTaskLink{}
	for j, link := range links {
		if used[j] {
			continue
		}
		link.Merge = mergeOfTask(link, wts)
		if link.MergedInto == "" && link.Merge == nil {
			continue // not merged, and its worktree is not in this repository
		}
		wts.MergedTasks = append(wts.MergedTasks, link)
	}
	wts.MainMerges = nil
}

// mergeOfTask looks in the merges of the main line for the one of this task: a branch merged with
// its default message names the branch; a task merged by lucode names its title. Messages a flow
// wrote itself cannot be recognised, and then only the note the merge left is known.
func mergeOfTask(t gitTaskLink, wts *GitWorktrees) *GitMergeRef {
	title := "Merge task: " + t.Title
	branch := "'" + t.Branch + "'"
	for _, c := range wts.MainMerges {
		if c.Subject == title || strings.HasPrefix(c.Subject, title) || (c.Merge && strings.Contains(c.Subject, branch)) || (c.Merge && strings.HasSuffix(c.Subject, " "+t.Branch)) {
			into := t.MergedInto
			if into == "" {
				into = wts.Main
			}
			kind := "squash"
			if c.Merge {
				kind = "merge"
			}
			return &GitMergeRef{Into: into, SHA: c.SHA, Subject: c.Subject, Author: c.Author, At: c.At, Kind: kind}
		}
	}
	return nil
}
