package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
	natspkg "claude-orchestrator/internal/natsbus"
)

func gitChange(ov GitOverview, path string) *GitChange {
	for i := range ov.Changes {
		if ov.Changes[i].Path == path {
			return &ov.Changes[i]
		}
	}
	return nil
}

func mustOverview(t *testing.T, repo string) GitOverview {
	t.Helper()
	ov, err := gitOverview(repo, repo)
	if err != nil {
		t.Fatal(err)
	}
	return ov
}

func mustAction(t *testing.T, repo string, q gitActionRequest) {
	t.Helper()
	if _, err := gitAction(repo, repo, q); err != nil {
		t.Fatalf("%s: %v", q.Action, err)
	}
}

func commitAll(t *testing.T, dir, msg string) {
	t.Helper()
	rgit(t, dir, "add", "--all")
	rgit(t, dir, "commit", "-q", "-m", msg)
}

// Status keeps the first column: gitIn trims output, which turned " M a.txt" into "M a.txt".
func TestOverviewReadsStagedUnstagedAndUntracked(t *testing.T) {
	repo, _ := reviewRepo(t)
	write(t, filepath.Join(repo, "a.txt"), "one\ntwo\n") // unstaged
	write(t, filepath.Join(repo, "s.txt"), "new\n")
	rgit(t, repo, "add", "s.txt") // staged
	write(t, filepath.Join(repo, "dir with space.txt"), "x\n")

	ov := mustOverview(t, repo)
	if ov.Branch != "main" || ov.Main != "main" || ov.Head == "" {
		t.Fatalf("branch %q main %q head %q", ov.Branch, ov.Main, ov.Head)
	}
	a := gitChange(ov, "a.txt")
	if a == nil || a.Index != "." || a.Worktree != "M" || a.UnstagedAdd != 1 {
		t.Fatalf("a.txt: %+v", a)
	}
	s := gitChange(ov, "s.txt")
	if s == nil || s.Index != "A" || s.Worktree != "." || s.StagedAdd != 1 {
		t.Fatalf("s.txt: %+v", s)
	}
	if u := gitChange(ov, "dir with space.txt"); u == nil || !u.Untracked {
		t.Fatalf("untracked: %+v", u)
	}
	if len(ov.Worktrees) != 2 || !ov.Worktrees[0].Main {
		t.Fatalf("worktrees: %+v", ov.Worktrees)
	}
	var taskBranch *GitBranch
	for i := range ov.Branches {
		if ov.Branches[i].Name == "task/x" {
			taskBranch = &ov.Branches[i]
		}
	}
	if taskBranch == nil || taskBranch.WorktreePath == "" {
		t.Fatalf("branches: %+v", ov.Branches)
	}
}

func TestStatusNamesARename(t *testing.T) {
	repo, _ := reviewRepo(t)
	rgit(t, repo, "mv", "a.txt", "renamed.txt")
	c := gitChange(mustOverview(t, repo), "renamed.txt")
	if c == nil || c.Index != "R" || c.OrigPath != "a.txt" {
		t.Fatalf("%+v", c)
	}
}

func TestLogHasParentsRefsAndFiles(t *testing.T) {
	repo, wt := reviewRepo(t)
	rgit(t, repo, "tag", "v1")
	commitOnBranch(t, wt)
	rgit(t, repo, "merge", "--no-ff", "-m", "Merge task: Add b", "task/x")
	write(t, filepath.Join(repo, "a.txt"), "one\nchanged\n")
	commitAll(t, repo, "subject line\n\nthe body")
	rgit(t, repo, "mv", "a.txt", "c.txt")
	commitAll(t, repo, "rename it")

	log, err := gitLog(repo, gitLogRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(log.Commits) != 5 || log.More {
		t.Fatalf("%d commits, more %v", len(log.Commits), log.More)
	}
	head := log.Commits[0]
	if head.Subject != "rename it" || head.FilesTotal != 1 || head.Files[0].Status != "R" || head.Files[0].Path != "c.txt" || head.Files[0].OrigPath != "a.txt" {
		t.Fatalf("head: %+v", head)
	}
	if len(head.Refs) == 0 || head.Refs[0].Name != "main" || !head.Refs[0].Head || head.Refs[0].Kind != "branch" {
		t.Fatalf("refs: %+v", head.Refs)
	}
	if log.Commits[1].Body != "the body" {
		t.Fatalf("body: %q", log.Commits[1].Body)
	}
	if log.Commits[1].Added != 1 || log.Commits[1].Files[0].Status != "M" {
		t.Fatalf("stat: %+v", log.Commits[1])
	}
	var merge, root GitCommit
	for _, c := range log.Commits {
		if strings.HasPrefix(c.Subject, "Merge task") {
			merge = c
		}
		if c.Subject == "init" {
			root = c
		}
	}
	if len(merge.Parents) != 2 || merge.FilesTotal != 1 || merge.Files[0].Path != "b.txt" {
		t.Fatalf("a merge lists what it brought to the main line: %+v", merge)
	}
	if len(root.Parents) != 0 || len(root.Refs) != 1 || root.Refs[0].Kind != "tag" {
		t.Fatalf("root: %+v", root)
	}

	limited, err := gitLog(repo, gitLogRequest{Limit: 2})
	if err != nil || len(limited.Commits) != 2 || !limited.More {
		t.Fatalf("limit: %+v %v", limited, err)
	}
	found, err := gitLog(repo, gitLogRequest{Grep: "RENAME"})
	if err != nil || len(found.Commits) != 1 {
		t.Fatalf("grep: %+v %v", found, err)
	}
	if _, err := gitLog(repo, gitLogRequest{Ref: "--all"}); err == nil {
		t.Fatal("an option passed as a ref must be refused")
	}
}

func TestLogOfAnEmptyRepository(t *testing.T) {
	repo := t.TempDir()
	rgit(t, repo, "init", "-q", "-b", "main")
	log, err := gitLog(repo, gitLogRequest{})
	if err != nil || len(log.Commits) != 0 {
		t.Fatalf("%+v %v", log, err)
	}
	ov := mustOverview(t, repo)
	if !ov.Unborn || ov.Branch != "main" {
		t.Fatalf("%+v", ov)
	}
}

func TestDiffModes(t *testing.T) {
	repo, wt := reviewRepo(t)
	write(t, filepath.Join(repo, "a.txt"), "one\nworktree\n")
	write(t, filepath.Join(repo, "new.txt"), "brand new\n")
	cases := []struct {
		name string
		req  gitDiffRequest
		want string
	}{
		{"worktree", gitDiffRequest{Mode: "worktree", Path: "a.txt"}, "+worktree"},
		{"untracked", gitDiffRequest{Mode: "untracked", Path: "new.txt"}, "+brand new"},
	}
	for _, c := range cases {
		d, err := gitDiff(repo, c.req)
		if err != nil || !strings.Contains(d.Text, c.want) {
			t.Fatalf("%s: %q %v", c.name, d.Text, err)
		}
	}
	rgit(t, repo, "add", "a.txt")
	if d, err := gitDiff(repo, gitDiffRequest{Mode: "staged", Path: "a.txt"}); err != nil || !strings.Contains(d.Text, "+worktree") {
		t.Fatalf("staged: %q %v", d.Text, err)
	}
	rgit(t, repo, "reset", "-q")
	rgit(t, repo, "checkout", "--", "a.txt")
	commitOnBranch(t, wt)
	if d, err := gitDiff(repo, gitDiffRequest{Mode: "commit", SHA: "task/x", Path: "b.txt"}); err != nil || !strings.Contains(d.Text, "+new") {
		t.Fatalf("commit: %q %v", d.Text, err)
	}
	if d, err := gitDiff(repo, gitDiffRequest{Mode: "range", Base: "main", Head: "task/x"}); err != nil || !strings.Contains(d.Text, "+new") {
		t.Fatalf("range: %q %v", d.Text, err)
	}
	if _, err := gitDiff(repo, gitDiffRequest{Mode: "worktree", Path: "../outside.txt"}); err == nil {
		t.Fatal("a path out of the repository must be refused")
	}
}

func TestCapDiffCutsOnALine(t *testing.T) {
	line := strings.Repeat("x", 1023) + "\n"
	d := capDiff(strings.Repeat(line, gitMaxDiff/1024+5))
	if !d.Truncated || len(d.Text) > gitMaxDiff || !strings.HasSuffix(d.Text, "\n") {
		t.Fatalf("truncated %v len %d", d.Truncated, len(d.Text))
	}
}

func TestCompareSaysAheadBehindAndConflicts(t *testing.T) {
	repo, wt := reviewRepo(t)
	write(t, filepath.Join(wt, "a.txt"), "from the branch\n")
	commitAll(t, wt, "branch edit")
	write(t, filepath.Join(repo, "a.txt"), "from main\n")
	commitAll(t, repo, "main edit")

	cmp, err := gitCompare(repo, gitCompareRequest{Base: "main", Head: "task/x"})
	if err != nil {
		t.Fatal(err)
	}
	if cmp.Ahead != 1 || cmp.Behind != 1 || cmp.MergeBase == "" {
		t.Fatalf("%+v", cmp)
	}
	if len(cmp.Commits) != 1 || cmp.Commits[0].Subject != "branch edit" || len(cmp.BaseCommits) != 1 {
		t.Fatalf("commits: %+v %+v", cmp.Commits, cmp.BaseCommits)
	}
	if !cmp.Conflicts || len(cmp.ConflictFiles) != 1 || cmp.ConflictFiles[0] != "a.txt" {
		t.Fatalf("conflicts: %v %v", cmp.Conflicts, cmp.ConflictFiles)
	}
	if cmp.FilesTotal != 1 || cmp.Files[0].Path != "a.txt" {
		t.Fatalf("files: %+v", cmp.Files)
	}
}

func TestStageUnstageDiscardCommit(t *testing.T) {
	repo, _ := reviewRepo(t)
	rgit(t, repo, "config", "user.email", "t@t")
	rgit(t, repo, "config", "user.name", "t")
	write(t, filepath.Join(repo, "a.txt"), "changed\n")
	write(t, filepath.Join(repo, "n.txt"), "new\n")

	mustAction(t, repo, gitActionRequest{Action: "stage", Paths: []string{"n.txt"}})
	if c := gitChange(mustOverview(t, repo), "n.txt"); c == nil || c.Index != "A" {
		t.Fatalf("stage: %+v", c)
	}
	mustAction(t, repo, gitActionRequest{Action: "unstage", Paths: []string{"n.txt"}})
	if c := gitChange(mustOverview(t, repo), "n.txt"); c == nil || !c.Untracked {
		t.Fatalf("unstage: %+v", c)
	}
	mustAction(t, repo, gitActionRequest{Action: "discard", Paths: []string{"a.txt"}})
	if b, _ := os.ReadFile(filepath.Join(repo, "a.txt")); string(b) != "one\n" {
		t.Fatalf("discard: %q", b)
	}
	mustAction(t, repo, gitActionRequest{Action: "clean", Paths: []string{"n.txt"}})
	if _, err := os.Stat(filepath.Join(repo, "n.txt")); err == nil {
		t.Fatal("clean left the file")
	}

	mustAction(t, repo, gitActionRequest{Action: "rm", Paths: []string{"a.txt"}})
	if c := gitChange(mustOverview(t, repo), "a.txt"); c == nil || c.Index != "D" {
		t.Fatalf("rm: %+v", c)
	}
	if _, err := gitAction(repo, repo, gitActionRequest{Action: "commit"}); err == nil {
		t.Fatal("a commit without a message went through")
	}
	mustAction(t, repo, gitActionRequest{Action: "commit", Message: "drop a"})
	if got := gitOut(t, repo, "log", "-1", "--format=%s"); got != "drop a" {
		t.Fatalf("commit: %q", got)
	}
	if _, err := gitAction(repo, repo, gitActionRequest{Action: "stage", Paths: []string{"../x"}}); err == nil {
		t.Fatal("a path out of the repository went through")
	}
}

func TestStashRoundTrip(t *testing.T) {
	repo, _ := reviewRepo(t)
	rgit(t, repo, "config", "user.email", "t@t")
	rgit(t, repo, "config", "user.name", "t")
	write(t, filepath.Join(repo, "a.txt"), "stashed\n")
	write(t, filepath.Join(repo, "u.txt"), "untracked\n")
	mustAction(t, repo, gitActionRequest{Action: "stash_push", Message: "wip", Include: true})

	ov := mustOverview(t, repo)
	if len(ov.Changes) != 0 || len(ov.Stashes) != 1 || !strings.Contains(ov.Stashes[0].Message, "wip") {
		t.Fatalf("%+v %+v", ov.Changes, ov.Stashes)
	}
	files, err := gitStashShow(repo, "stash@{0}")
	if err != nil || len(files.Files) != 1 || len(files.Untracked) != 1 || files.Untracked[0].Path != "u.txt" {
		t.Fatalf("%+v %v", files, err)
	}
	if d, err := gitDiff(repo, gitDiffRequest{Mode: "stash", SHA: "stash@{0}", Path: "u.txt", Untracked: true}); err != nil || !strings.Contains(d.Text, "+untracked") {
		t.Fatalf("untracked part: %q %v", d.Text, err)
	}
	if d, err := gitDiff(repo, gitDiffRequest{Mode: "stash", SHA: "stash@{0}", Path: "a.txt"}); err != nil || !strings.Contains(d.Text, "+stashed") {
		t.Fatalf("tracked part: %q %v", d.Text, err)
	}
	mustAction(t, repo, gitActionRequest{Action: "stash_pop", Stash: "stash@{0}"})
	ov = mustOverview(t, repo)
	if len(ov.Stashes) != 0 || gitChange(ov, "a.txt") == nil || gitChange(ov, "u.txt") == nil {
		t.Fatalf("pop: %+v", ov)
	}
}

// A merge that stops on conflicts stays stopped for a person, and abort undoes it.
func TestMergeConflictStaysAndAborts(t *testing.T) {
	repo, wt := reviewRepo(t)
	rgit(t, repo, "config", "user.email", "t@t")
	rgit(t, repo, "config", "user.name", "t")
	write(t, filepath.Join(wt, "a.txt"), "branch\n")
	commitAll(t, wt, "branch edit")
	write(t, filepath.Join(repo, "a.txt"), "main\n")
	commitAll(t, repo, "main edit")

	if _, err := gitAction(repo, repo, gitActionRequest{Action: "merge", Ref: "task/x"}); err == nil {
		t.Fatal("the merge should stop on the conflict")
	}
	ov := mustOverview(t, repo)
	if ov.State != "merging" {
		t.Fatalf("state %q", ov.State)
	}
	if c := gitChange(ov, "a.txt"); c == nil || !c.Conflict {
		t.Fatalf("%+v", c)
	}
	mustAction(t, repo, gitActionRequest{Action: "abort"})
	if ov := mustOverview(t, repo); ov.State != "" || len(ov.Changes) != 0 {
		t.Fatalf("after abort: %+v", ov)
	}
}

func TestBranchAndTagActions(t *testing.T) {
	repo, _ := reviewRepo(t)
	rgit(t, repo, "config", "user.email", "t@t")
	rgit(t, repo, "config", "user.name", "t")
	mustAction(t, repo, gitActionRequest{Action: "branch_create", Name: "feature", Switch: true})
	if ov := mustOverview(t, repo); ov.Branch != "feature" {
		t.Fatalf("on %q", ov.Branch)
	}
	mustAction(t, repo, gitActionRequest{Action: "checkout", Ref: "main"})
	mustAction(t, repo, gitActionRequest{Action: "branch_rename", Ref: "feature", Name: "other"})
	mustAction(t, repo, gitActionRequest{Action: "branch_delete", Ref: "other"})
	mustAction(t, repo, gitActionRequest{Action: "tag", Name: "v1", Message: "first"})
	if ov := mustOverview(t, repo); len(ov.Tags) != 1 || ov.Tags[0].Name != "v1" || ov.Tags[0].SHA != ov.Head {
		t.Fatalf("an annotated tag points at its commit: %+v head %s", ov.Tags, ov.Head)
	}
	mustAction(t, repo, gitActionRequest{Action: "tag_delete", Ref: "v1"})
	if _, err := gitAction(repo, repo, gitActionRequest{Action: "checkout", Ref: "-f"}); err == nil {
		t.Fatal("an option passed as a ref went through")
	}
	if _, err := gitAction(repo, repo, gitActionRequest{Action: "nope"}); err == nil {
		t.Fatal("an unknown action went through")
	}
}

func TestWorktreesSayWhereTheirWorkLanded(t *testing.T) {
	repo, wt := reviewRepo(t)
	commitOnBranch(t, wt)
	res, err := gitWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	if res.Main != "main" || len(res.Worktrees) != 2 || !res.Worktrees[0].Main {
		t.Fatalf("%+v", res)
	}
	w := res.Worktrees[1]
	if w.Branch != "task/x" || w.Ahead != 1 || w.Merge != nil || w.Subject != "add b" {
		t.Fatalf("before the merge: %+v", w)
	}
	rgit(t, repo, "merge", "--no-ff", "-m", "Merge task: Add b", "task/x")
	write(t, filepath.Join(repo, "z.txt"), "later\n")
	commitAll(t, repo, "later work on main")
	write(t, filepath.Join(wt, "dirty.txt"), "x\n")

	res, err = gitWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	w = res.Worktrees[1]
	if w.Merge == nil || w.Merge.Kind != "merge" || w.Merge.Subject != "Merge task: Add b" || w.Merge.Into != "main" {
		t.Fatalf("the merge commit, not the commits after it: %+v", w.Merge)
	}
	if w.Dirty != 1 || w.Ahead != 0 {
		t.Fatalf("%+v", w)
	}
	if _, err := gitAction(repo, repo, gitActionRequest{Action: "worktree_remove", Path: wt}); err == nil {
		t.Fatal("removed a worktree with uncommitted work without being told to")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatal("the refused removal deleted it anyway")
	}
	if _, err := gitAction(repo, repo, gitActionRequest{Action: "worktree_remove", Path: repo, Force: true}); err == nil {
		t.Fatal("removed the main checkout")
	}
	rgit(t, wt, "worktree", "lock", wt)
	mustAction(t, repo, gitActionRequest{Action: "worktree_remove", Path: wt, Force: true})
	if _, err := os.Stat(wt); err == nil {
		t.Fatal("the worktree is still there")
	}
}

// A branch with no commits of its own has no merge commit to point at.
func TestAFreshWorktreeIsOnlyContained(t *testing.T) {
	repo, _ := reviewRepo(t)
	res, err := gitWorktrees(repo)
	if err != nil {
		t.Fatal(err)
	}
	if m := res.Worktrees[1].Merge; m == nil || m.Kind != "contained" {
		t.Fatalf("%+v", m)
	}
}

func TestATargetCannotPointOutsideItsRepository(t *testing.T) {
	repo, wt := reviewRepo(t)
	other, _ := reviewRepo(t)
	h := &Handler{}
	if dir, main, err := h.resolveGitTarget(gitTarget{Repo: repo, Worktree: wt}); err != nil || !samePath(dir, wt) || !samePath(main, repo) {
		t.Fatalf("own worktree: %q %q %v", dir, main, err)
	}
	if _, _, err := h.resolveGitTarget(gitTarget{Repo: repo, Worktree: other}); err == nil {
		t.Fatal("accepted a worktree of another repository")
	}
	// opened from a linked worktree, it is still the same repository
	if _, main, err := h.resolveGitTarget(gitTarget{Repo: wt}); err != nil || !samePath(main, repo) {
		t.Fatalf("%q %v", main, err)
	}
	if _, _, err := h.resolveGitTarget(gitTarget{Repo: t.TempDir()}); err != errNotARepo {
		t.Fatalf("a plain directory: %v", err)
	}
}

// ---- through the hub, on a throwaway NATS, with the repository only on the runner ----

func gitHubCall(t *testing.T, h *Handler, pid, op string, body map[string]any, out any) int {
	t.Helper()
	raw, _ := json.Marshal(body)
	mux := http.NewServeMux()
	h.registerGit(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/projects/"+pid+"/git/"+op, bytes.NewReader(raw)))
	if out != nil && rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s: %v\n%s", op, err, rec.Body.String())
		}
	}
	if rec.Code != http.StatusOK {
		t.Logf("%s: %d %s", op, rec.Code, rec.Body.String())
	}
	return rec.Code
}

func TestGitTabThroughHubAndRunner(t *testing.T) {
	h, pid, card, repo, wt, _ := gitFlowHub(t, func(r *Handler) http.Handler {
		return NewRunnerHandler(r.db, r.bus, r.manager, "")
	})
	if err := dbpkg.UpdateProjectEnvDirs(h.db, pid, map[string][]string{"git-runner": {repo}}); err != nil {
		t.Fatal(err)
	}
	target := func(extra map[string]any) map[string]any {
		m := map[string]any{"runner_id": "git-runner", "repo": repo}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	var ov GitOverview
	if code := gitHubCall(t, h, pid, "overview", target(nil), &ov); code != 200 || ov.Branch != "main" || len(ov.Worktrees) != 2 {
		t.Fatalf("overview %d: %+v", code, ov)
	}
	var log GitLog
	if code := gitHubCall(t, h, pid, "log", target(nil), &log); code != 200 || len(log.Commits) != 2 {
		t.Fatalf("log %d: %+v", code, log)
	}

	// the worktree is shown with its task, which only the hub knows
	var wts GitWorktrees
	if code := gitHubCall(t, h, pid, "worktrees", target(nil), &wts); code != 200 {
		t.Fatalf("worktrees %d", code)
	}
	// the session row is what ties a card to its branch
	sess := dbpkg.Session{ID: "s1", ProjectID: pid, Name: "s", Model: "haiku", Status: "idle", CardID: card.ID}
	if err := dbpkg.InsertSession(h.db, sess); err != nil {
		t.Fatal(err)
	}
	gitHubCall(t, h, pid, "worktrees", target(nil), &wts)
	var linked *GitWorktree
	for i := range wts.Worktrees {
		if samePath(wts.Worktrees[i].Path, wt) {
			linked = &wts.Worktrees[i]
		}
	}
	if linked == nil || linked.Task == nil || linked.Task.Title != card.Title || linked.Task.CardID != card.ID {
		t.Fatalf("worktree: %+v", linked)
	}

	// acting on a linked worktree, from the browser, reaches that checkout on the runner
	write(t, filepath.Join(wt, "t.txt"), "x\n")
	if code := gitHubCall(t, h, pid, "action", target(map[string]any{"worktree": wt, "action": "stage", "paths": []string{"t.txt"}}), nil); code != 200 {
		t.Fatalf("stage %d", code)
	}
	if got := gitOut(t, wt, "status", "--porcelain"); !strings.HasPrefix(got, "A") {
		t.Fatalf("status in the worktree: %q", got)
	}

	// a directory that is not the project's is refused before it reaches any runner
	if code := gitHubCall(t, h, pid, "log", map[string]any{"runner_id": "git-runner", "repo": t.TempDir()}, nil); code != http.StatusForbidden {
		t.Fatalf("foreign dir: %d", code)
	}
	if code := gitHubCall(t, h, pid, "log", map[string]any{"runner_id": "", "repo": repo}, nil); code != http.StatusForbidden {
		t.Fatalf("no runner, which would mean the hub's own disk: %d", code)
	}
	if code := gitHubCall(t, h, pid, "rm-rf", target(nil), nil); code != http.StatusNotFound {
		t.Fatalf("unknown op: %d", code)
	}
}

// The tool the Supervisor follows a reference with: from the hub to the runner, never the hub's disk.
func TestGitDiffToolThroughHubAndRunner(t *testing.T) {
	h, pid, _, repo, wt, _ := gitFlowHub(t, func(r *Handler) http.Handler {
		return NewRunnerHandler(r.db, r.bus, r.manager, "")
	})
	if err := dbpkg.UpdateProjectEnvDirs(h.db, pid, map[string][]string{"git-runner": {repo}}); err != nil {
		t.Fatal(err)
	}
	sess := dbpkg.Session{ID: "orch", ProjectID: pid, Name: "o", Model: "haiku", Status: "idle", Kind: dbpkg.SessionKindOrchestrator}
	if err := dbpkg.InsertSession(h.db, sess); err != nil {
		t.Fatal(err)
	}
	call := func(args map[string]any) (string, error) {
		raw, _ := json.Marshal(args)
		return h.HandleTaskTool("orch", "mcp__lucode__git_diff", raw)
	}

	// no runner and no repo: the project's first target
	got, err := call(map[string]any{"mode": "range", "base": "main", "head": "task/x", "path": "b.txt"})
	if err != nil || !strings.Contains(got, "+new") {
		t.Fatalf("range: %q %v", got, err)
	}
	got, err = call(map[string]any{"mode": "commit", "sha": "task/x", "runner_id": "git-runner", "repo": repo})
	if err != nil || !strings.Contains(got, "b.txt") {
		t.Fatalf("commit: %q %v", got, err)
	}
	write(t, filepath.Join(wt, "t.txt"), "fresh\n")
	got, err = call(map[string]any{"mode": "untracked", "path": "t.txt", "worktree": wt})
	if err != nil || !strings.Contains(got, "+fresh") {
		t.Fatalf("a linked worktree: %q %v", got, err)
	}
	if got, err = call(map[string]any{"mode": "worktree"}); err != nil || got != "No differences." {
		t.Fatalf("nothing changed: %q %v", got, err)
	}

	if _, err = call(map[string]any{"mode": "worktree", "repo": t.TempDir()}); err == nil {
		t.Fatal("read a directory that is not the project's")
	}
	if _, err = call(map[string]any{"mode": "worktree", "runner_id": "someone-else"}); err == nil {
		t.Fatal("read from a runner the project does not use")
	}
	if _, err = call(map[string]any{"mode": "worktree", "worktree": t.TempDir()}); err == nil {
		t.Fatal("read a worktree of another repository")
	}
	if _, err = call(map[string]any{"mode": "worktree", "path": "../secret"}); err == nil {
		t.Fatal("read a path out of the repository")
	}
	if _, err = call(map[string]any{"mode": "commit", "sha": "--all"}); err == nil {
		t.Fatal("passed an option as a commit")
	}
}

func TestGitDiffToolWithoutARunnerSaysWhy(t *testing.T) {
	h, pid, _ := newTaskTestHandler(t)
	h.bus = natspkg.NewBus(nil)
	_, err := h.gitDiffTool(pid, json.RawMessage(`{"mode":"worktree"}`))
	if err == nil || !strings.Contains(err.Error(), "runner") {
		t.Fatalf("%v", err)
	}
}

func TestAnOldRunnerIsNamedByTheGitTab(t *testing.T) {
	h, pid, _, repo, _, _ := gitFlowHub(t, oldRunner)
	if err := dbpkg.UpdateProjectEnvDirs(h.db, pid, map[string][]string{"git-runner": {repo}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"runner_id": "git-runner", "repo": repo})
	mux := http.NewServeMux()
	h.registerGit(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/projects/"+pid+"/git/overview", bytes.NewReader(raw)))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "older than the Git tab") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestLinkWorktreeTasksFindsAMergeByItsMessage(t *testing.T) {
	wts := GitWorktrees{Main: "main",
		Worktrees: []GitWorktree{{Path: "/r", Main: true}, {Path: "/w/one", Branch: "task/one"}},
		MainMerges: []GitMainCommit{
			{SHA: "aaa", Subject: "Merge task: Gone task", Merge: true},
			{SHA: "bbb", Subject: "Merge branch 'task/manual' into main", Merge: true},
			{SHA: "ccc", Subject: "Merge task: Squashed"},
		},
	}
	links := []gitTaskLink{
		{CardID: "1", Title: "One", Branch: "task/one", Worktree: "/w/one"},
		{CardID: "2", Title: "Gone task", Branch: "task/gone", MergedInto: "main"},
		{CardID: "3", Title: "By hand", Branch: "task/manual"},
		{CardID: "4", Title: "Squashed", Branch: "task/sq", MergedInto: "main"},
		{CardID: "5", Title: "Still open", Branch: "task/elsewhere"},
		{CardID: "6", Title: "Flow message", Branch: "task/flow", MergedInto: "dev"},
	}
	linkWorktreeTasks(&wts, links)
	if wts.Worktrees[1].Task == nil || wts.Worktrees[1].Task.CardID != "1" {
		t.Fatalf("%+v", wts.Worktrees[1])
	}
	got := map[string]gitTaskLink{}
	for _, l := range wts.MergedTasks {
		got[l.CardID] = l
	}
	if len(got) != 4 {
		t.Fatalf("a task neither merged nor here is left out: %+v", wts.MergedTasks)
	}
	if got["2"].Merge == nil || got["2"].Merge.SHA != "aaa" || got["2"].Merge.Kind != "merge" {
		t.Fatalf("%+v", got["2"].Merge)
	}
	if got["3"].Merge == nil || got["3"].Merge.SHA != "bbb" {
		t.Fatalf("%+v", got["3"].Merge)
	}
	if got["4"].Merge == nil || got["4"].Merge.Kind != "squash" {
		t.Fatalf("%+v", got["4"].Merge)
	}
	if got["6"].Merge != nil || got["6"].MergedInto != "dev" {
		t.Fatalf("no commit to point at, only the note: %+v", got["6"])
	}
	if wts.MainMerges != nil {
		t.Fatal("the raw merges are for the hub, not the browser")
	}
}
