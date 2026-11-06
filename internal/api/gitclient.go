package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The Git tab, runner side. The repository is on the disk of a RUNNER, so everything here runs
// there and is served at POST /api/git/* over the RPC bridge. The hub (gitproject.go) checks the
// project owns the directory, relays the call and adds what only it knows: which task a worktree
// belongs to. Nothing in this file touches the database.

const (
	gitReadTimeout = 60 * time.Second
	gitNetTimeout  = 5 * time.Minute
	// An RPC reply over the NATS limit is lost without an error and the hub just times out, and
	// its body travels in base64. So every answer stays well under 1 MB.
	gitMaxReply = 680 * 1024
	gitMaxDiff  = 400 * 1024
)

// errNotARepo means the directory exists but git does not know it: the tab offers nothing to do.
var errNotARepo = errors.New("not a git repository")

// gitTarget is where a call acts. Repo is a directory of the project on this runner (relative ones
// resolve against --working-dir, as for a turn); Worktree optionally selects one of its linked
// worktrees and is only accepted if git lists it.
type gitTarget struct {
	Repo     string `json:"repo"`
	Worktree string `json:"worktree,omitempty"`
}

// gitRaw runs git without trimming: the first column of porcelain output means something. On a
// non-zero exit the error carries stderr and the exit code is returned alongside.
func gitRaw(dir string, timeout time.Duration, args ...string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	full := append([]string{"-C", dir, "-c", "core.quotepath=off", "-c", "color.ui=false"}, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", // a push without credentials fails instead of waiting forever
		"GCM_INTERACTIVE=never",
		"GIT_OPTIONAL_LOCKS=0", // looking must not fight an agent working in the same repository
		"GIT_EDITOR=true",
		"GIT_MERGE_AUTOEDIT=no",
		"GIT_PAGER=cat",
	)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	if err == nil {
		return out.String(), 0, nil
	}
	code := -1
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	}
	msg := strings.TrimSpace(errb.String())
	if msg == "" {
		msg = strings.TrimSpace(out.String())
	}
	if ctx.Err() == context.DeadlineExceeded {
		msg = "git took too long and was stopped: " + strings.Join(args, " ")
	}
	if msg == "" {
		msg = err.Error()
	}
	return out.String(), code, errors.New(msg)
}

func gitRead(dir string, args ...string) (string, error) {
	out, _, err := gitRaw(dir, gitReadTimeout, args...)
	return out, err
}

// resolve answers the directory to run git in and the repository's main checkout.
func (h *Handler) resolveGitTarget(t gitTarget) (dir, repo string, err error) {
	path := t.Repo
	if path == "" {
		return "", "", errors.New("no repository given")
	}
	if h.manager != nil {
		path = h.manager.ResolveWorkDirs([]string{path})[0]
	}
	if _, err := os.Stat(path); err != nil {
		return "", "", errors.New("the directory does not exist on this runner: " + path)
	}
	top, err := gitRead(path, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", errNotARepo
	}
	repo = filepath.FromSlash(strings.TrimSpace(top))
	if main, ok := repoOfWorktree(repo); ok {
		repo = main
	}
	if t.Worktree == "" {
		return repo, repo, nil
	}
	out, err := gitRead(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return "", "", err
	}
	for _, wt := range parseWorktrees(out) {
		if samePath(wt.path, t.Worktree) {
			return filepath.FromSlash(wt.path), repo, nil
		}
	}
	return "", "", errors.New("that worktree does not belong to this repository: " + t.Worktree)
}

// ---- argument safety: nothing a browser sends may be read as an option by git ----

var (
	gitStashRe = regexp.MustCompile(`^stash@\{\d+\}$`)
	gitBadRef  = regexp.MustCompile(`[\x00-\x20\x7f~^:?*\[\\]|\.\.`)
)

// safeRef accepts a branch, tag or sha, optionally with the "stash@{n}" form or a trailing ^/~.
func safeRef(ref string) error {
	if gitStashRe.MatchString(ref) {
		return nil
	}
	if ref == "" || strings.HasPrefix(ref, "-") || gitBadRef.MatchString(ref) {
		return errors.New("not a valid git reference: " + ref)
	}
	return nil
}

// safePaths keeps paths inside the working tree. They always go after "--" as well.
func safePaths(paths []string) error {
	for _, p := range paths {
		clean := filepath.ToSlash(p)
		if p == "" || filepath.IsAbs(p) || strings.HasPrefix(clean, "/") || strings.HasPrefix(clean, "../") ||
			strings.Contains(clean, "/../") || strings.HasSuffix(clean, "/..") || clean == ".." {
			return errors.New("not a path inside the repository: " + p)
		}
	}
	return nil
}

// ---- overview ----

// GitChange is one line of git status. Index and Worktree are the two status columns ("." = no
// change on that side); the counts come from numstat of each side.
type GitChange struct {
	Path        string `json:"path"`
	OrigPath    string `json:"orig_path,omitempty"`
	Index       string `json:"index"`
	Worktree    string `json:"worktree"`
	Conflict    bool   `json:"conflict,omitempty"`
	Untracked   bool   `json:"untracked,omitempty"`
	StagedAdd   int    `json:"staged_add,omitempty"`
	StagedDel   int    `json:"staged_del,omitempty"`
	UnstagedAdd int    `json:"unstaged_add,omitempty"`
	UnstagedDel int    `json:"unstaged_del,omitempty"`
}

type GitBranch struct {
	Name         string `json:"name"`
	Remote       bool   `json:"remote,omitempty"`
	SHA          string `json:"sha"`
	Upstream     string `json:"upstream,omitempty"`
	UpstreamGone bool   `json:"upstream_gone,omitempty"`
	Ahead        int    `json:"ahead,omitempty"`  // against its upstream
	Behind       int    `json:"behind,omitempty"` // against its upstream
	MainAhead    int    `json:"main_ahead"`       // commits not in the main branch
	MainBehind   int    `json:"main_behind"`      // commits of the main branch it lacks
	At           int64  `json:"at"`
	Author       string `json:"author"`
	Subject      string `json:"subject"`
	Current      bool   `json:"current,omitempty"`
	WorktreePath string `json:"worktree_path,omitempty"` // checked out there
}

type GitTag struct {
	Name    string `json:"name"`
	SHA     string `json:"sha"`
	At      int64  `json:"at"`
	Subject string `json:"subject"`
}

type GitStash struct {
	Ref     string `json:"ref"` // stash@{n}
	SHA     string `json:"sha"`
	Message string `json:"message"`
	At      int64  `json:"at"`
	Author  string `json:"author"`
	Email   string `json:"email,omitempty"`
}

type GitWorktreeBrief struct {
	Path   string `json:"path"`
	Branch string `json:"branch,omitempty"`
	Main   bool   `json:"main,omitempty"`
}

type GitOverview struct {
	NotRepo   bool               `json:"not_repo,omitempty"`
	Repo      string             `json:"repo"`
	Dir       string             `json:"dir"`
	Branch    string             `json:"branch"`   // "" when detached or on an unborn branch
	Detached  bool               `json:"detached"` //
	Unborn    bool               `json:"unborn,omitempty"`
	Head      string             `json:"head"`
	Upstream  string             `json:"upstream,omitempty"`
	Ahead     int                `json:"ahead"`
	Behind    int                `json:"behind"`
	State     string             `json:"state,omitempty"` // merging, rebasing, cherry-picking, reverting, bisecting
	Main      string             `json:"main"`            // the branch work ends up in
	Changes   []GitChange        `json:"changes"`
	Truncated bool               `json:"truncated,omitempty"` // too many changes to list them all
	Branches  []GitBranch        `json:"branches"`
	Tags      []GitTag           `json:"tags"`
	Stashes   []GitStash         `json:"stashes"`
	Worktrees []GitWorktreeBrief `json:"worktrees"`
	Remotes   []string           `json:"remotes"`
	UserName  string             `json:"user_name,omitempty"`
	UserEmail string             `json:"user_email,omitempty"`
}

const gitMaxChanges = 2000

// parseStatusV2 reads git status --porcelain=v2 -z --branch.
func parseStatusV2(out string, ov *GitOverview) {
	parts := strings.Split(out, "\x00")
	for i := 0; i < len(parts); i++ {
		l := parts[i]
		if l == "" {
			continue
		}
		switch l[0] {
		case '#':
			f := strings.Fields(l)
			if len(f) < 3 {
				continue
			}
			switch f[1] {
			case "branch.oid":
				if f[2] != "(initial)" {
					ov.Head = f[2]
				} else {
					ov.Unborn = true
				}
			case "branch.head":
				if f[2] == "(detached)" {
					ov.Detached = true
				} else {
					ov.Branch = f[2]
				}
			case "branch.upstream":
				ov.Upstream = f[2]
			case "branch.ab":
				if len(f) >= 4 {
					ov.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[2], "+"))
					ov.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[3], "-"))
				}
			}
		case '1', '2', 'u':
			fields := map[byte]int{'1': 9, '2': 10, 'u': 11}[l[0]]
			f := strings.SplitN(l, " ", fields)
			if len(f) < fields {
				continue
			}
			c := GitChange{Path: f[fields-1], Index: f[1][:1], Worktree: f[1][1:]}
			switch l[0] {
			case '2':
				i++ // the original name comes as the next NUL-separated field
				if i < len(parts) {
					c.OrigPath = parts[i]
				}
			case 'u':
				c.Conflict = true
			}
			if len(ov.Changes) < gitMaxChanges {
				ov.Changes = append(ov.Changes, c)
			} else {
				ov.Truncated = true
			}
		case '?':
			if len(ov.Changes) < gitMaxChanges {
				ov.Changes = append(ov.Changes, GitChange{Path: l[2:], Index: ".", Worktree: "?", Untracked: true})
			} else {
				ov.Truncated = true
			}
		}
	}
}

// numstatZ reads git diff --numstat -z: added, removed and the path (the new name of a rename).
func numstatZ(out string) map[string][2]int {
	res := map[string][2]int{}
	parts := strings.Split(out, "\x00")
	for i := 0; i < len(parts); i++ {
		f := strings.SplitN(parts[i], "\t", 3)
		if len(f) < 3 {
			continue
		}
		a, _ := strconv.Atoi(f[0])
		d, _ := strconv.Atoi(f[1])
		path := f[2]
		if path == "" && i+2 < len(parts) { // a rename: old and new follow as separate fields
			path = parts[i+2]
			i += 2
		}
		res[path] = [2]int{a, d}
	}
	return res
}

func gitStateOf(dir string) string {
	names := []struct{ path, state string }{
		{"MERGE_HEAD", "merging"},
		{"rebase-merge", "rebasing"},
		{"rebase-apply", "rebasing"},
		{"CHERRY_PICK_HEAD", "cherry-picking"},
		{"REVERT_HEAD", "reverting"},
		{"BISECT_LOG", "bisecting"},
	}
	args := []string{"rev-parse"}
	for _, n := range names {
		args = append(args, "--git-path", n.path)
	}
	out, err := gitRead(dir, args...)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i, n := range names {
		if i >= len(lines) {
			break
		}
		p := strings.TrimSpace(lines[i])
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		if _, err := os.Stat(p); err == nil {
			return n.state
		}
	}
	return ""
}

// gitMainBranch is the branch work ends up in: what origin/HEAD says, main or master, or else what
// the main checkout has out.
func gitMainBranch(repo string) string {
	if out, err := gitRead(repo, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD"); err == nil {
		name := strings.TrimPrefix(strings.TrimSpace(out), "refs/remotes/origin/")
		if _, err := gitRead(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); err == nil {
			return name
		}
	}
	for _, name := range []string{"main", "master"} {
		if _, err := gitRead(repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+name); err == nil {
			return name
		}
	}
	out, _ := gitRead(repo, "symbolic-ref", "--quiet", "--short", "HEAD")
	return strings.TrimSpace(out)
}

// gitRefs lists branches and tags. ahead-behind needs git 2.41: older ones answer without it.
func gitRefs(dir, main string) ([]GitBranch, []GitTag) {
	const base = "%(refname)%1f%(objectname)%1f%(*objectname)%1f%(upstream:short)%1f%(upstream:track,nobracket)%1f%(committerdate:unix)%1f%(creatordate:unix)%1f%(authorname)%1f%(subject)%1f%(HEAD)%1f%(worktreepath)"
	withAB := main != ""
	format := base
	if withAB {
		format += "%1f%(ahead-behind:refs/heads/" + main + ")"
	}
	refs := []string{"for-each-ref", "--sort=-committerdate", "--count=800", "--format=" + format, "refs/heads", "refs/remotes", "refs/tags"}
	out, err := gitRead(dir, refs...)
	if err != nil && withAB {
		withAB = false
		refs[3] = "--format=" + base
		out, err = gitRead(dir, refs...)
	}
	branches, tags := []GitBranch{}, []GitTag{}
	if err != nil {
		return branches, tags
	}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(l, "\r"), "\x1f")
		if len(f) < 11 {
			continue
		}
		at, _ := strconv.ParseInt(f[5], 10, 64)
		switch {
		case strings.HasPrefix(f[0], "refs/tags/"):
			sha := f[1]
			if f[2] != "" {
				sha = f[2] // an annotated tag: the commit it points at
			}
			created, _ := strconv.ParseInt(f[6], 10, 64)
			tags = append(tags, GitTag{Name: strings.TrimPrefix(f[0], "refs/tags/"), SHA: sha, At: created, Subject: f[8]})
		case strings.HasSuffix(f[0], "/HEAD") && strings.HasPrefix(f[0], "refs/remotes/"):
			// origin/HEAD is a pointer, not a branch
		default:
			b := GitBranch{SHA: f[1], Upstream: f[3], At: at, Author: f[7], Subject: f[8], Current: f[9] == "*", WorktreePath: f[10]}
			if strings.HasPrefix(f[0], "refs/remotes/") {
				b.Remote = true
				b.Name = strings.TrimPrefix(f[0], "refs/remotes/")
			} else {
				b.Name = strings.TrimPrefix(f[0], "refs/heads/")
			}
			parseTrack(f[4], &b)
			if withAB && len(f) >= 12 {
				if ab := strings.Fields(f[11]); len(ab) == 2 {
					b.MainAhead, _ = strconv.Atoi(ab[0])
					b.MainBehind, _ = strconv.Atoi(ab[1])
				}
			}
			branches = append(branches, b)
		}
	}
	return branches, tags
}

// parseTrack reads %(upstream:track,nobracket): "ahead 1, behind 2" or "gone".
func parseTrack(s string, b *GitBranch) {
	if s == "gone" {
		b.UpstreamGone = true
		return
	}
	for _, part := range strings.Split(s, ",") {
		f := strings.Fields(part)
		if len(f) != 2 {
			continue
		}
		n, _ := strconv.Atoi(f[1])
		switch f[0] {
		case "ahead":
			b.Ahead = n
		case "behind":
			b.Behind = n
		}
	}
}

func gitStashes(dir string) []GitStash {
	res := []GitStash{}
	out, err := gitRead(dir, "stash", "list", "--format=%gd%x1f%H%x1f%gs%x1f%at%x1f%an%x1f%ae")
	if err != nil {
		return res
	}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimRight(l, "\r"), "\x1f")
		if len(f) < 6 {
			continue
		}
		at, _ := strconv.ParseInt(f[3], 10, 64)
		res = append(res, GitStash{Ref: f[0], SHA: f[1], Message: f[2], At: at, Author: f[4], Email: f[5]})
	}
	return res
}

func gitOverview(dir, repo string) (GitOverview, error) {
	ov := GitOverview{Repo: repo, Dir: dir, Changes: []GitChange{}, Branches: []GitBranch{}, Tags: []GitTag{}, Stashes: []GitStash{}, Worktrees: []GitWorktreeBrief{}, Remotes: []string{}}
	out, err := gitRead(dir, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all")
	if err != nil {
		return ov, err
	}
	parseStatusV2(out, &ov)
	if s, err := gitRead(dir, "diff", "--numstat", "-z"); err == nil {
		stats := numstatZ(s)
		for i := range ov.Changes {
			if st, ok := stats[ov.Changes[i].Path]; ok {
				ov.Changes[i].UnstagedAdd, ov.Changes[i].UnstagedDel = st[0], st[1]
			}
		}
	}
	if s, err := gitRead(dir, "diff", "--cached", "--numstat", "-z"); err == nil {
		stats := numstatZ(s)
		for i := range ov.Changes {
			if st, ok := stats[ov.Changes[i].Path]; ok {
				ov.Changes[i].StagedAdd, ov.Changes[i].StagedDel = st[0], st[1]
			}
		}
	}
	ov.State = gitStateOf(dir)
	ov.Main = gitMainBranch(repo)
	ov.Branches, ov.Tags = gitRefs(dir, ov.Main)
	ov.Stashes = gitStashes(dir)
	if s, err := gitRead(repo, "worktree", "list", "--porcelain"); err == nil {
		for i, wt := range parseWorktrees(s) {
			ov.Worktrees = append(ov.Worktrees, GitWorktreeBrief{Path: wt.path, Branch: wt.branch, Main: i == 0})
		}
	}
	if s, err := gitRead(repo, "remote"); err == nil {
		for _, r := range strings.Fields(s) {
			ov.Remotes = append(ov.Remotes, r)
		}
	}
	if s, err := gitRead(dir, "config", "user.name"); err == nil {
		ov.UserName = strings.TrimSpace(s)
	}
	if s, err := gitRead(dir, "config", "user.email"); err == nil {
		ov.UserEmail = strings.TrimSpace(s)
	}
	return ov, nil
}

// ---- log ----

type GitRef struct {
	Name string `json:"name"`
	Kind string `json:"kind"`           // branch, remote, tag, head
	Head bool   `json:"head,omitempty"` // HEAD points at this branch
}

type GitFileStat struct {
	Path     string `json:"path"`
	OrigPath string `json:"orig_path,omitempty"`
	Status   string `json:"status"` // A M D R C T
	Added    int    `json:"added"`
	Removed  int    `json:"removed"`
	Binary   bool   `json:"binary,omitempty"`
}

type GitCommit struct {
	SHA       string   `json:"sha"`
	Parents   []string `json:"parents"`
	Author    string   `json:"author"`
	Email     string   `json:"email"`
	At        int64    `json:"at"`
	Committer string   `json:"committer"`
	// CommitterEmail is the address that goes with Committer.
	CommitterEmail string        `json:"committer_email,omitempty"`
	CommitAt       int64         `json:"commit_at"`
	Refs           []GitRef      `json:"refs"`
	Subject        string        `json:"subject"`
	Body           string        `json:"body,omitempty"`
	Files          []GitFileStat `json:"files"`
	FilesTotal     int           `json:"files_total"`
	Added          int           `json:"added"`
	Removed        int           `json:"removed"`
}

const gitLogFormat = "--format=%x1e%H%x1f%P%x1f%an%x1f%ae%x1f%at%x1f%cn%x1f%ct%x1f%D%x1f%s%x1f%b%x1f%ce%x1f"

// parseDecoration reads %D with --decorate=full.
func parseDecoration(d string) []GitRef {
	refs := []GitRef{}
	if strings.TrimSpace(d) == "" {
		return refs
	}
	for _, part := range strings.Split(d, ", ") {
		part = strings.TrimSpace(part)
		head := false
		if strings.HasPrefix(part, "HEAD -> ") {
			head = true
			part = strings.TrimPrefix(part, "HEAD -> ")
		}
		part = strings.TrimPrefix(part, "tag: ")
		switch {
		case part == "HEAD":
			refs = append(refs, GitRef{Name: "HEAD", Kind: "head", Head: true})
		case strings.HasPrefix(part, "refs/heads/"):
			refs = append(refs, GitRef{Name: strings.TrimPrefix(part, "refs/heads/"), Kind: "branch", Head: head})
		case strings.HasPrefix(part, "refs/remotes/"):
			name := strings.TrimPrefix(part, "refs/remotes/")
			if !strings.HasSuffix(name, "/HEAD") {
				refs = append(refs, GitRef{Name: name, Kind: "remote"})
			}
		case strings.HasPrefix(part, "refs/tags/"):
			refs = append(refs, GitRef{Name: strings.TrimPrefix(part, "refs/tags/"), Kind: "tag"})
		}
	}
	return refs
}

func unquoteGitPath(p string) string {
	if strings.HasPrefix(p, `"`) {
		if s, err := strconv.Unquote(p); err == nil {
			return s
		}
	}
	return p
}

// parseStats reads the --raw and --numstat blocks git prints after a commit, or for a diff. Both
// list the files in the same order; raw has the exact names and the status letter, numstat the
// line counts. Only the first cap files are kept; the totals count all of them.
func parseStats(text string, fileCap int) (files []GitFileStat, total, added, removed int) {
	files = []GitFileStat{}
	idx := 0
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimRight(l, "\r")
		if l == "" {
			continue
		}
		if l[0] == ':' {
			tab := strings.Split(l, "\t")
			head := strings.Fields(tab[0])
			if len(head) < 5 || len(tab) < 2 {
				continue
			}
			total++
			if len(files) >= fileCap {
				continue
			}
			fs := GitFileStat{Status: head[4][:1], Path: unquoteGitPath(tab[1])}
			if len(tab) >= 3 {
				fs.OrigPath, fs.Path = fs.Path, unquoteGitPath(tab[2])
			}
			files = append(files, fs)
			continue
		}
		f := strings.SplitN(l, "\t", 3)
		if len(f) < 3 {
			continue
		}
		a, errA := strconv.Atoi(f[0])
		d, errD := strconv.Atoi(f[1])
		added += a
		removed += d
		if idx < len(files) {
			files[idx].Added, files[idx].Removed = a, d
			files[idx].Binary = errA != nil && errD != nil
		}
		idx++
	}
	return files, total, added, removed
}

// parseLog reads the output of git log with gitLogFormat, --raw and --numstat.
func parseLog(out string, fileCap int) []GitCommit {
	commits := []GitCommit{}
	for _, rec := range strings.Split(out, "\x1e") {
		f := strings.SplitN(rec, "\x1f", 12)
		if len(f) < 12 {
			continue
		}
		at, _ := strconv.ParseInt(f[4], 10, 64)
		cat, _ := strconv.ParseInt(f[6], 10, 64)
		c := GitCommit{
			SHA: f[0], Parents: strings.Fields(f[1]), Author: f[2], Email: f[3], At: at,
			Committer: f[5], CommitAt: cat, Refs: parseDecoration(f[7]), Subject: f[8],
			Body: strings.TrimSpace(f[9]), CommitterEmail: f[10],
		}
		c.Files, c.FilesTotal, c.Added, c.Removed = parseStats(f[11], fileCap)
		commits = append(commits, c)
	}
	return commits
}

type gitLogRequest struct {
	gitTarget
	Ref    string `json:"ref,omitempty"` // "" = every branch, remote and tag
	Limit  int    `json:"limit,omitempty"`
	Skip   int    `json:"skip,omitempty"`
	Grep   string `json:"grep,omitempty"`
	Author string `json:"author,omitempty"`
	Path   string `json:"path,omitempty"`
}

type GitLog struct {
	Commits []GitCommit `json:"commits"`
	More    bool        `json:"more"`
}

func gitLogArgs(limit int) []string {
	return []string{"log", "--decorate=full", gitLogFormat, "--raw", "--numstat", "-M", "--diff-merges=first-parent", "--no-abbrev", "-n", strconv.Itoa(limit)}
}

func gitLog(dir string, req gitLogRequest) (GitLog, error) {
	res := GitLog{Commits: []GitCommit{}}
	limit := req.Limit
	if limit <= 0 || limit > 500 {
		limit = 150
	}
	args := append(gitLogArgs(limit+1), "--topo-order", "--skip="+strconv.Itoa(max(req.Skip, 0)))
	if req.Grep != "" {
		args = append(args, "--grep="+req.Grep, "--regexp-ignore-case", "--fixed-strings")
	}
	if req.Author != "" {
		args = append(args, "--author="+req.Author, "--regexp-ignore-case", "--fixed-strings")
	}
	if req.Ref != "" {
		if err := safeRef(req.Ref); err != nil {
			return res, err
		}
		args = append(args, req.Ref)
	} else {
		args = append(args, "--branches", "--remotes", "--tags")
		if _, err := gitRead(dir, "rev-parse", "--verify", "--quiet", "HEAD"); err == nil {
			args = append(args, "HEAD")
		}
	}
	if req.Path != "" {
		if err := safePaths([]string{req.Path}); err != nil {
			return res, err
		}
		args = append(args, "--", req.Path)
	} else {
		args = append(args, "--")
	}
	out, err := gitRead(dir, args...)
	if err != nil {
		if strings.Contains(err.Error(), "does not have any commits") {
			return res, nil
		}
		return res, err
	}
	res.Commits = parseLog(out, 40)
	if len(res.Commits) > limit {
		res.Commits, res.More = res.Commits[:limit], true
	}
	return res, nil
}

// gitCommitDetail is one commit with its whole file list.
func gitCommitDetail(dir, sha string) (GitCommit, error) {
	if err := safeRef(sha); err != nil {
		return GitCommit{}, err
	}
	out, err := gitRead(dir, append(gitLogArgs(1), sha, "--")...)
	if err != nil {
		return GitCommit{}, err
	}
	commits := parseLog(out, 3000)
	if len(commits) == 0 {
		return GitCommit{}, errors.New("no such commit: " + sha)
	}
	return commits[0], nil
}

// ---- diff ----

type gitDiffRequest struct {
	gitTarget
	Mode      string `json:"mode"` // worktree, staged, untracked, commit, range, stash
	Path      string `json:"path,omitempty"`
	OrigPath  string `json:"orig_path,omitempty"`
	SHA       string `json:"sha,omitempty"`       // commit or stash@{n}
	Base      string `json:"base,omitempty"`      // range
	Head      string `json:"head,omitempty"`      // range
	Untracked bool   `json:"untracked,omitempty"` // stash: the file is in its untracked part
}

type GitDiff struct {
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

func gitDiff(dir string, req gitDiffRequest) (GitDiff, error) {
	paths := []string{}
	if req.Path != "" {
		paths = append(paths, req.Path)
		if req.OrigPath != "" {
			paths = append(paths, req.OrigPath)
		}
	}
	if err := safePaths(paths); err != nil {
		return GitDiff{}, err
	}
	var args []string
	switch req.Mode {
	case "worktree":
		args = []string{"diff", "--find-renames"}
	case "staged":
		args = []string{"diff", "--cached", "--find-renames"}
	case "untracked":
		if req.Path == "" {
			return GitDiff{}, errors.New("a path is needed")
		}
		// --no-index exits 1 when the files differ, which is the whole point here
		out, code, err := gitRaw(dir, gitReadTimeout, "diff", "--no-index", "--", os.DevNull, req.Path)
		if err != nil && code != 1 {
			return GitDiff{}, err
		}
		return capDiff(out), nil
	case "commit":
		if err := safeRef(req.SHA); err != nil {
			return GitDiff{}, err
		}
		args = []string{"show", "--format=", "--find-renames", "--diff-merges=first-parent", req.SHA}
	case "range":
		if err := safeRef(req.Base); err != nil {
			return GitDiff{}, err
		}
		if err := safeRef(req.Head); err != nil {
			return GitDiff{}, err
		}
		args = []string{"diff", "--find-renames", req.Base + "..." + req.Head}
	case "stash":
		if !gitStashRe.MatchString(req.SHA) {
			return GitDiff{}, errors.New("not a stash: " + req.SHA)
		}
		if req.Untracked {
			args = []string{"show", "--format=", req.SHA + "^3"}
		} else {
			args = []string{"diff", "--find-renames", req.SHA + "^1", req.SHA}
		}
	default:
		return GitDiff{}, errors.New("unknown diff mode: " + req.Mode)
	}
	args = append(append(args, "--"), paths...)
	out, err := gitRead(dir, args...)
	if err != nil {
		return GitDiff{}, err
	}
	return capDiff(out), nil
}

func capDiff(out string) GitDiff {
	if len(out) <= gitMaxDiff {
		return GitDiff{Text: out}
	}
	cut := strings.LastIndexByte(out[:gitMaxDiff], '\n')
	if cut < 0 {
		cut = gitMaxDiff
	}
	return GitDiff{Text: out[:cut+1], Truncated: true}
}

// ---- compare ----

type gitCompareRequest struct {
	gitTarget
	Base string `json:"base"`
	Head string `json:"head"`
}

type GitCompare struct {
	Base          string        `json:"base"`
	Head          string        `json:"head"`
	MergeBase     string        `json:"merge_base"`
	Ahead         int           `json:"ahead"`  // commits in head that are not in base
	Behind        int           `json:"behind"` // commits in base that are not in head
	Commits       []GitCommit   `json:"commits"`
	BaseCommits   []GitCommit   `json:"base_commits"`
	Files         []GitFileStat `json:"files"`
	FilesTotal    int           `json:"files_total"`
	Added         int           `json:"added"`
	Removed       int           `json:"removed"`
	Conflicts     bool          `json:"conflicts"`
	ConflictFiles []string      `json:"conflict_files"`
}

func gitCompare(dir string, req gitCompareRequest) (GitCompare, error) {
	res := GitCompare{Base: req.Base, Head: req.Head, Commits: []GitCommit{}, BaseCommits: []GitCommit{}, Files: []GitFileStat{}, ConflictFiles: []string{}}
	if err := safeRef(req.Base); err != nil {
		return res, err
	}
	if err := safeRef(req.Head); err != nil {
		return res, err
	}
	out, err := gitRead(dir, "rev-list", "--left-right", "--count", req.Base+"..."+req.Head)
	if err != nil {
		return res, err
	}
	if f := strings.Fields(out); len(f) == 2 {
		res.Behind, _ = strconv.Atoi(f[0])
		res.Ahead, _ = strconv.Atoi(f[1])
	}
	if mb, err := gitRead(dir, "merge-base", req.Base, req.Head); err == nil {
		res.MergeBase = strings.TrimSpace(mb)
	}
	if s, err := gitRead(dir, append(gitLogArgs(100), req.Base+".."+req.Head, "--")...); err == nil {
		res.Commits = parseLog(s, 20)
	}
	if s, err := gitRead(dir, append(gitLogArgs(100), req.Head+".."+req.Base, "--")...); err == nil {
		res.BaseCommits = parseLog(s, 20)
	}
	if s, err := gitRead(dir, "diff", "--raw", "--numstat", "-M", "--no-abbrev", req.Base+"..."+req.Head, "--"); err == nil {
		res.Files, res.FilesTotal, res.Added, res.Removed = parseStats(s, 1500)
	}
	if res.Ahead > 0 {
		// merged in memory, nothing changes on disk. Exit 1 = conflicts, listed after the tree id.
		s, code, err := gitRaw(dir, gitReadTimeout, "merge-tree", "--write-tree", "--name-only", req.Base, req.Head)
		if err != nil && code == 1 {
			res.Conflicts = true
			lines := strings.Split(s, "\n")
			for _, l := range lines[min(1, len(lines)):] {
				l = strings.TrimSpace(l)
				if l == "" {
					break
				}
				res.ConflictFiles = append(res.ConflictFiles, l)
			}
		}
	}
	return res, nil
}

// ---- stash files ----

type gitStashRequest struct {
	gitTarget
	Stash string `json:"stash"`
}

type GitStashFiles struct {
	Files     []GitFileStat `json:"files"`
	Untracked []GitFileStat `json:"untracked"`
}

func gitStashShow(dir, stash string) (GitStashFiles, error) {
	res := GitStashFiles{Files: []GitFileStat{}, Untracked: []GitFileStat{}}
	if !gitStashRe.MatchString(stash) {
		return res, errors.New("not a stash: " + stash)
	}
	s, err := gitRead(dir, "diff", "--raw", "--numstat", "-M", "--no-abbrev", stash+"^1", stash, "--")
	if err != nil {
		return res, err
	}
	res.Files, _, _, _ = parseStats(s, 1500)
	// the third parent exists only for a stash made with --include-untracked
	if s, err := gitRead(dir, "show", "--format=", "--raw", "--numstat", "--no-abbrev", stash+"^3", "--"); err == nil {
		res.Untracked, _, _, _ = parseStats(s, 1500)
	}
	return res, nil
}

// ---- worktrees ----

// GitMergeRef says where a branch's work landed on the main line.
type GitMergeRef struct {
	Into    string `json:"into"`
	SHA     string `json:"sha,omitempty"`
	Subject string `json:"subject,omitempty"`
	Author  string `json:"author,omitempty"`
	Email   string `json:"email,omitempty"`
	At      int64  `json:"at,omitempty"`
	// merge: a merge commit brought it in. contained: the tip is on the main line itself (a
	// fast-forward, or a branch with no commits of its own). squash: found by its message only.
	Kind string `json:"kind"`
}

type GitWorktree struct {
	Path       string       `json:"path"`
	Branch     string       `json:"branch,omitempty"`
	Head       string       `json:"head"`
	Detached   bool         `json:"detached,omitempty"`
	Main       bool         `json:"main,omitempty"`
	Locked     bool         `json:"locked,omitempty"`
	LockReason string       `json:"lock_reason,omitempty"`
	Prunable   bool         `json:"prunable,omitempty"`
	Missing    bool         `json:"missing,omitempty"` // the directory is gone
	Dirty      int          `json:"dirty"`             // files with uncommitted changes
	Ahead      int          `json:"ahead"`             // commits not in the main branch
	Behind     int          `json:"behind"`            // commits of the main branch it lacks
	Subject    string       `json:"subject,omitempty"`
	Author     string       `json:"author,omitempty"`
	Email      string       `json:"email,omitempty"`
	At         int64        `json:"at,omitempty"`
	Merge      *GitMergeRef `json:"merge,omitempty"`
	Task       *gitTaskLink `json:"task,omitempty"` // only the hub knows this
}

// GitMainCommit is a commit of the main line that looks like a merge, for the hub to match a task
// whose branch is gone against its message.
type GitMainCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	Email   string `json:"email,omitempty"`
	At      int64  `json:"at"`
	Merge   bool   `json:"merge"` // has two parents
}

type GitWorktrees struct {
	Main        string          `json:"main"`
	Worktrees   []GitWorktree   `json:"worktrees"`
	MainMerges  []GitMainCommit `json:"main_merges,omitempty"`
	MergedTasks []gitTaskLink   `json:"merged_tasks"` // tasks whose worktree no longer exists
}

// findMerge finds how tip reached base: the oldest commit of base's first-parent line that
// descends from tip. If tip is on that line itself there is no merge commit to point at.
func findMerge(repo, base, tip string) *GitMergeRef {
	if _, err := gitRead(repo, "merge-base", "--is-ancestor", tip, base); err != nil {
		return nil
	}
	first, err := gitRead(repo, "rev-list", "--first-parent", "-n", "5000", base)
	if err != nil {
		return nil
	}
	anc, _ := gitRead(repo, "rev-list", "--ancestry-path", tip+".."+base)
	descendant := map[string]bool{}
	for _, s := range strings.Fields(anc) {
		descendant[s] = true
	}
	found, kind := "", "merge"
	for _, sha := range strings.Fields(first) {
		if sha == tip {
			found, kind = sha, "contained"
			break
		}
		if descendant[sha] {
			found = sha
		} else if found != "" {
			break
		}
	}
	if found == "" {
		return nil
	}
	ref := &GitMergeRef{Into: base, SHA: found, Kind: kind}
	if s, err := gitRead(repo, "show", "-s", "--format=%s%x1f%an%x1f%ae%x1f%at", found); err == nil {
		if f := strings.Split(strings.TrimSpace(s), "\x1f"); len(f) == 4 {
			ref.Subject, ref.Author, ref.Email = f[0], f[1], f[2]
			ref.At, _ = strconv.ParseInt(f[3], 10, 64)
		}
	}
	return ref
}

func gitWorktrees(repo string) (GitWorktrees, error) {
	res := GitWorktrees{Worktrees: []GitWorktree{}, MergedTasks: []gitTaskLink{}}
	out, err := gitRead(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return res, err
	}
	res.Main = gitMainBranch(repo)
	checkedOut, _ := gitRead(repo, "symbolic-ref", "--quiet", "--short", "HEAD")
	checkedOut = strings.TrimSpace(checkedOut)
	for i, wt := range parseWorktrees(out) {
		w := GitWorktree{Path: wt.path, Branch: wt.branch, Head: wt.head, Detached: wt.detached, Main: i == 0,
			Locked: wt.locked, LockReason: wt.lockReason, Prunable: wt.prunable}
		if _, err := os.Stat(wt.path); err != nil {
			w.Missing = true
		} else if s, err := gitRead(wt.path, "status", "--porcelain"); err == nil {
			if t := strings.TrimRight(s, "\r\n"); t != "" {
				w.Dirty = strings.Count(t, "\n") + 1
			}
		}
		if wt.head != "" {
			if s, err := gitRead(repo, "show", "-s", "--format=%s%x1f%an%x1f%ae%x1f%at", wt.head); err == nil {
				if f := strings.Split(strings.TrimSpace(s), "\x1f"); len(f) == 4 {
					w.Subject, w.Author, w.Email = f[0], f[1], f[2]
					w.At, _ = strconv.ParseInt(f[3], 10, 64)
				}
			}
			if res.Main != "" && !w.Main {
				if s, err := gitRead(repo, "rev-list", "--left-right", "--count", res.Main+"..."+wt.head); err == nil {
					if f := strings.Fields(s); len(f) == 2 {
						w.Behind, _ = strconv.Atoi(f[0])
						w.Ahead, _ = strconv.Atoi(f[1])
					}
				}
				w.Merge = findMerge(repo, res.Main, wt.head)
				// tasks merge into whatever the main checkout has out, which need not be main
				if w.Merge == nil && checkedOut != "" && checkedOut != res.Main && checkedOut != wt.branch {
					w.Merge = findMerge(repo, checkedOut, wt.head)
				}
			}
		}
		res.Worktrees = append(res.Worktrees, w)
	}
	res.MainMerges = gitMainMerges(repo, res.Main, checkedOut)
	return res, nil
}

// gitMainMerges lists the recent merges of the main line and of what the main checkout has out.
func gitMainMerges(repo string, refs ...string) []GitMainCommit {
	var res []GitMainCommit
	seen := map[string]bool{}
	for _, ref := range refs {
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		out, err := gitRead(repo, "log", "--first-parent", "-n", "1000", "--format=%H%x1f%P%x1f%an%x1f%ae%x1f%at%x1f%s", ref, "--")
		if err != nil {
			continue
		}
		for _, l := range strings.Split(out, "\n") {
			f := strings.SplitN(strings.TrimRight(l, "\r"), "\x1f", 6)
			if len(f) < 6 || seen[f[0]] {
				continue
			}
			merge := len(strings.Fields(f[1])) > 1
			if !merge && !strings.HasPrefix(f[5], "Merge ") {
				continue // a squash from a task keeps "Merge task:" unless a flow renamed it
			}
			seen[f[0]] = true
			at, _ := strconv.ParseInt(f[4], 10, 64)
			res = append(res, GitMainCommit{SHA: f[0], Subject: f[5], Author: f[2], Email: f[3], At: at, Merge: merge})
		}
	}
	sort.SliceStable(res, func(i, j int) bool { return res[i].At > res[j].At })
	if len(res) > 800 {
		res = res[:800]
	}
	return res
}

// ---- RPC ----

// gitReply writes the answer, or says it is too large rather than letting the RPC lose it.
func gitReply(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(b) > gitMaxReply {
		http.Error(w, "the answer is too large to send from the runner: narrow it down", http.StatusRequestEntityTooLarge)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(b)
}

// gitServe decodes the request, resolves its target and answers with what fn returns. Failures of
// git are 409, never 404: on this route a 404 only means the runner predates it.
func gitServe[R any](h *Handler, w http.ResponseWriter, r *http.Request, target func(*R) gitTarget, fn func(dir, repo string, req R) (any, error)) {
	var req R
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	dir, repo, err := h.resolveGitTarget(target(&req))
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	res, err := fn(dir, repo, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	gitReply(w, res)
}

func (h *Handler) registerGitRPC(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/git/overview", func(w http.ResponseWriter, r *http.Request) {
		var t gitTarget
		if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		dir, repo, err := h.resolveGitTarget(t)
		if errors.Is(err, errNotARepo) {
			gitReply(w, GitOverview{NotRepo: true, Dir: t.Repo})
			return
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		ov, err := gitOverview(dir, repo)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		gitReply(w, ov)
	})
	mux.HandleFunc("POST /api/git/log", func(w http.ResponseWriter, r *http.Request) {
		gitServe(h, w, r, func(q *gitLogRequest) gitTarget { return q.gitTarget }, func(dir, _ string, q gitLogRequest) (any, error) {
			res, err := gitLog(dir, q)
			if err != nil {
				return nil, err
			}
			// a history of huge commits: drop the file lists before dropping the answer
			if b, _ := json.Marshal(res); len(b) > gitMaxReply {
				for i := range res.Commits {
					if len(res.Commits[i].Files) > 3 {
						res.Commits[i].Files = res.Commits[i].Files[:3]
					}
				}
			}
			return res, nil
		})
	})
	mux.HandleFunc("POST /api/git/commit", func(w http.ResponseWriter, r *http.Request) {
		type commitReq struct {
			gitTarget
			SHA string `json:"sha"`
		}
		gitServe(h, w, r, func(q *commitReq) gitTarget { return q.gitTarget }, func(dir, _ string, q commitReq) (any, error) {
			return gitCommitDetail(dir, q.SHA)
		})
	})
	mux.HandleFunc("POST /api/git/diff", func(w http.ResponseWriter, r *http.Request) {
		gitServe(h, w, r, func(q *gitDiffRequest) gitTarget { return q.gitTarget }, func(dir, _ string, q gitDiffRequest) (any, error) {
			return gitDiff(dir, q)
		})
	})
	mux.HandleFunc("POST /api/git/compare", func(w http.ResponseWriter, r *http.Request) {
		gitServe(h, w, r, func(q *gitCompareRequest) gitTarget { return q.gitTarget }, func(dir, _ string, q gitCompareRequest) (any, error) {
			return gitCompare(dir, q)
		})
	})
	mux.HandleFunc("POST /api/git/stash", func(w http.ResponseWriter, r *http.Request) {
		gitServe(h, w, r, func(q *gitStashRequest) gitTarget { return q.gitTarget }, func(dir, _ string, q gitStashRequest) (any, error) {
			return gitStashShow(dir, q.Stash)
		})
	})
	mux.HandleFunc("POST /api/git/worktrees", func(w http.ResponseWriter, r *http.Request) {
		gitServe(h, w, r, func(q *gitTarget) gitTarget { return *q }, func(_, repo string, _ gitTarget) (any, error) {
			return gitWorktrees(repo)
		})
	})
	mux.HandleFunc("POST /api/git/action", func(w http.ResponseWriter, r *http.Request) {
		gitServe(h, w, r, func(q *gitActionRequest) gitTarget { return q.gitTarget }, func(dir, repo string, q gitActionRequest) (any, error) {
			return gitAction(dir, repo, q)
		})
	})
}
