package api

import (
	"errors"
	"os"
	"strings"
)

// What the Git tab can change, on the runner that holds the repository. Each action is one or two
// git commands; when git refuses, its own message goes back to the user and nothing is retried or
// forced. A merge that stops on conflicts stays stopped: that is the state a person resolves.

type gitActionRequest struct {
	gitTarget
	Action  string   `json:"action"`
	Paths   []string `json:"paths,omitempty"`
	Message string   `json:"message,omitempty"`
	Amend   bool     `json:"amend,omitempty"`
	Ref     string   `json:"ref,omitempty"`   // branch, tag or commit it acts on
	Name    string   `json:"name,omitempty"`  // a new branch or tag
	Start   string   `json:"start,omitempty"` // where a new branch starts
	Force   bool     `json:"force,omitempty"` // only ever sent after the user confirmed it
	NoFF    bool     `json:"no_ff,omitempty"`
	Detach  bool     `json:"detach,omitempty"`
	Switch  bool     `json:"switch,omitempty"` // a new branch: also check it out
	Include bool     `json:"include_untracked,omitempty"`
	Keep    bool     `json:"keep_index,omitempty"`
	Stash   string   `json:"stash,omitempty"`
	Path    string   `json:"path,omitempty"` // a worktree
}

type GitActionResult struct {
	Output string `json:"output"`
}

func needPaths(paths []string) error {
	if len(paths) == 0 {
		return errors.New("no files given")
	}
	return safePaths(paths)
}

// gitAction runs one action in dir (the checkout it was aimed at). A worktree is removed from the
// main checkout, repo.
func gitAction(dir, repo string, q gitActionRequest) (GitActionResult, error) {
	run := func(args ...string) (GitActionResult, error) {
		// the long limit: a commit or a merge runs the repository's hooks
		out, _, err := gitRaw(dir, gitNetTimeout, args...)
		return GitActionResult{Output: strings.TrimSpace(out)}, err
	}
	withPaths := func(args ...string) (GitActionResult, error) {
		if err := needPaths(q.Paths); err != nil {
			return GitActionResult{}, err
		}
		return run(append(append(args, "--"), q.Paths...)...)
	}
	checkRefs := func(refs ...string) error {
		for _, r := range refs {
			if err := safeRef(r); err != nil {
				return err
			}
		}
		return nil
	}
	checkStash := func() error {
		if !gitStashRe.MatchString(q.Stash) {
			return errors.New("not a stash: " + q.Stash)
		}
		return nil
	}

	switch q.Action {
	case "stage":
		if len(q.Paths) == 0 {
			return run("add", "--all")
		}
		return withPaths("add", "--all")
	case "unstage":
		if len(q.Paths) == 0 {
			return run("reset", "--quiet")
		}
		return withPaths("reset", "--quiet")
	case "discard": // back to the index: what is staged stays
		return withPaths("restore", "--worktree")
	case "clean": // files git does not track are gone for good
		return withPaths("clean", "--force")
	case "rm":
		return withPaths("rm", "--force")
	case "rm_cached": // stop tracking, keep the file
		return withPaths("rm", "--cached", "--force")

	case "commit":
		if strings.TrimSpace(q.Message) == "" && !q.Amend {
			return GitActionResult{}, errors.New("a commit needs a message")
		}
		args := []string{"commit"}
		if q.Amend {
			args = append(args, "--amend")
		}
		if strings.TrimSpace(q.Message) != "" {
			args = append(args, "-m", q.Message)
		} else {
			args = append(args, "--no-edit")
		}
		return run(args...)

	case "checkout":
		if err := checkRefs(q.Ref); err != nil {
			return GitActionResult{}, err
		}
		if q.Detach {
			return run("switch", "--detach", q.Ref)
		}
		return run("switch", q.Ref)
	case "branch_create":
		if err := checkRefs(q.Name); err != nil {
			return GitActionResult{}, err
		}
		start := q.Start
		if start == "" {
			start = "HEAD"
		} else if err := checkRefs(start); err != nil {
			return GitActionResult{}, err
		}
		if q.Switch {
			return run("switch", "-c", q.Name, start)
		}
		return run("branch", q.Name, start)
	case "branch_delete":
		if err := checkRefs(q.Ref); err != nil {
			return GitActionResult{}, err
		}
		flag := "-d"
		if q.Force {
			flag = "-D"
		}
		return run("branch", flag, q.Ref)
	case "branch_rename":
		if err := checkRefs(q.Ref, q.Name); err != nil {
			return GitActionResult{}, err
		}
		return run("branch", "-m", q.Ref, q.Name)

	case "merge":
		if err := checkRefs(q.Ref); err != nil {
			return GitActionResult{}, err
		}
		args := []string{"merge", "--no-edit"}
		if q.NoFF {
			args = append(args, "--no-ff")
		}
		return run(append(args, q.Ref)...)
	case "cherry_pick":
		if err := checkRefs(q.Ref); err != nil {
			return GitActionResult{}, err
		}
		return run("cherry-pick", q.Ref)
	case "revert":
		if err := checkRefs(q.Ref); err != nil {
			return GitActionResult{}, err
		}
		return run("revert", "--no-edit", q.Ref)
	case "abort":
		switch gitStateOf(dir) {
		case "merging":
			return run("merge", "--abort")
		case "rebasing":
			return run("rebase", "--abort")
		case "cherry-picking":
			return run("cherry-pick", "--abort")
		case "reverting":
			return run("revert", "--abort")
		}
		return GitActionResult{}, errors.New("there is nothing in progress to abort")

	case "tag":
		if err := checkRefs(q.Name); err != nil {
			return GitActionResult{}, err
		}
		args := []string{"tag"}
		if strings.TrimSpace(q.Message) != "" {
			args = append(args, "-a", "-m", q.Message)
		}
		args = append(args, q.Name)
		if q.Ref != "" {
			if err := checkRefs(q.Ref); err != nil {
				return GitActionResult{}, err
			}
			args = append(args, q.Ref)
		}
		return run(args...)
	case "tag_delete":
		if err := checkRefs(q.Ref); err != nil {
			return GitActionResult{}, err
		}
		return run("tag", "--delete", q.Ref)

	case "stash_push":
		args := []string{"stash", "push"}
		if q.Include {
			args = append(args, "--include-untracked")
		}
		if q.Keep {
			args = append(args, "--keep-index")
		}
		if strings.TrimSpace(q.Message) != "" {
			args = append(args, "-m", q.Message)
		}
		return run(args...)
	case "stash_apply":
		if err := checkStash(); err != nil {
			return GitActionResult{}, err
		}
		return run("stash", "apply", q.Stash)
	case "stash_pop":
		if err := checkStash(); err != nil {
			return GitActionResult{}, err
		}
		return run("stash", "pop", q.Stash)
	case "stash_drop":
		if err := checkStash(); err != nil {
			return GitActionResult{}, err
		}
		return run("stash", "drop", q.Stash)

	case "fetch":
		out, _, err := gitRaw(dir, gitNetTimeout, "fetch", "--all", "--prune")
		return GitActionResult{Output: strings.TrimSpace(out)}, err
	case "pull":
		out, _, err := gitRaw(dir, gitNetTimeout, "pull", "--ff-only")
		return GitActionResult{Output: strings.TrimSpace(out)}, err
	case "push":
		return gitPush(dir)

	case "worktree_remove":
		return gitRemoveWorktree(repo, q.Path, q.Force)
	case "worktree_prune":
		out, err := gitRead(repo, "worktree", "prune", "--verbose")
		return GitActionResult{Output: strings.TrimSpace(out)}, err
	}
	return GitActionResult{}, errors.New("unknown action: " + q.Action)
}

// gitPush pushes the current branch, setting its upstream the first time. It never forces.
func gitPush(dir string) (GitActionResult, error) {
	args := []string{"push"}
	if _, err := gitRead(dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err != nil {
		branch, err := gitRead(dir, "symbolic-ref", "--quiet", "--short", "HEAD")
		if err != nil {
			return GitActionResult{}, errors.New("HEAD is detached: check out a branch to push it")
		}
		remotes, _ := gitRead(dir, "remote")
		list := strings.Fields(remotes)
		if len(list) == 0 {
			return GitActionResult{}, errors.New("this repository has no remote to push to")
		}
		remote := list[0]
		for _, r := range list {
			if r == "origin" {
				remote = r
			}
		}
		args = append(args, "--set-upstream", remote, strings.TrimSpace(branch))
	}
	// git writes the outcome of a push to stderr; a success has nothing else to show
	out, _, err := gitRaw(dir, gitNetTimeout, args...)
	return GitActionResult{Output: strings.TrimSpace(out)}, err
}

// gitRemoveWorktree removes a linked worktree. The main checkout is refused, and uncommitted work
// is refused unless the user confirmed losing it. The lock claude leaves on its worktrees goes only
// with that same confirmation, or when the worktree is clean.
func gitRemoveWorktree(repo, path string, force bool) (GitActionResult, error) {
	if path == "" {
		return GitActionResult{}, errors.New("no worktree given")
	}
	out, err := gitRead(repo, "worktree", "list", "--porcelain")
	if err != nil {
		return GitActionResult{}, err
	}
	wts := parseWorktrees(out)
	for i, wt := range wts {
		if !samePath(wt.path, path) {
			continue
		}
		if i == 0 {
			return GitActionResult{}, errors.New("that is the main checkout, not a linked worktree")
		}
		if _, statErr := os.Stat(wt.path); statErr == nil && !force {
			if s, err := gitRead(wt.path, "status", "--porcelain"); err != nil {
				return GitActionResult{}, err
			} else if strings.TrimSpace(s) != "" {
				return GitActionResult{}, errors.New("this worktree has uncommitted changes")
			}
		}
		_, _, _ = gitRaw(repo, gitReadTimeout, "worktree", "unlock", wt.path)
		if _, _, err := gitRaw(repo, gitReadTimeout, "worktree", "remove", "--force", wt.path); err != nil {
			return GitActionResult{}, err
		}
		return GitActionResult{}, nil
	}
	return GitActionResult{}, errors.New("that worktree does not belong to this repository: " + path)
}
