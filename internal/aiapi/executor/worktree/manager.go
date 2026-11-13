// Package worktree implements the lazy-worktree lifecycle used by the
// executor to isolate the model's filesystem edits from the user's
// working dir. The manager is created once per turn; the worktree is
// created on the first filesystem call (lazy) and destroyed at the end
// of the turn (after the apply phase).
//
// If the user's workDir is not a git repo, the manager is in
// "passthrough" mode: every operation targets the userWorkDir directly,
// and there is no isolation. This is the only safe fallback for
// non-git projects.
package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Manager owns the per-turn worktree lifecycle. It is safe for concurrent
// use (the executor may issue tool calls from a single goroutine today,
// but the mutex makes future parallelism safe).
type Manager struct {
	userWorkDir string
	sessionID   string
	branchName  string

	mu           sync.Mutex
	worktreePath string
	enabled      bool
}

// NewManager creates a manager for the given workDir and session.
func NewManager(userWorkDir, sessionID string) *Manager {
	m := &Manager{
		userWorkDir: userWorkDir,
		sessionID:   sessionID,
		branchName:  "lucode/" + sanitizeBranchName(sessionID),
	}
	m.enabled = isGitRepo(userWorkDir)
	return m
}

// IsGitRepo reports whether the worktree manager is in worktree mode.
func (m *Manager) IsGitRepo() bool {
	return m.enabled
}

// Ensure creates the worktree if it doesn't exist yet.
func (m *Manager) Ensure() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.enabled {
		return m.userWorkDir, nil
	}
	if m.worktreePath != "" {
		return m.worktreePath, nil
	}

	worktreePath := filepath.Join(m.userWorkDir, ".lucode", "worktree", m.sessionID)
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0755); err != nil {
		return "", err
	}

	cmd := exec.Command("git", "worktree", "add", worktreePath, "-b", m.branchName, "HEAD")
	cmd.Dir = m.userWorkDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree add: %s: %w", strings.TrimSpace(string(out)), err)
	}

	m.worktreePath = worktreePath
	return worktreePath, nil
}

// Path returns the path the executor should target right now.
func (m *Manager) Path() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled || m.worktreePath == "" {
		return m.userWorkDir
	}
	return m.worktreePath
}

// Active reports whether a worktree has been created.
func (m *Manager) Active() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled && m.worktreePath != ""
}

// Cleanup removes the worktree and deletes the branch. Safe to call
// multiple times. The user's workDir is left untouched.
func (m *Manager) Cleanup() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.enabled || m.worktreePath == "" {
		return nil
	}
	worktreePath := m.worktreePath

	defer func() { m.worktreePath = "" }()

	if out, err := exec.Command("git", "worktree", "remove", worktreePath, "--force").
		CombinedOutput(); err != nil {
		_ = out
	}
	// Belt-and-suspenders: on Windows, `git worktree remove` sometimes
	// leaves the directory behind (e.g. when the test process still has
	// file handles). Force-remove it here.
	if err := os.RemoveAll(worktreePath); err != nil && !os.IsNotExist(err) {
		_ = err
	}

	if out, err := exec.Command("git", "branch", "-D", m.branchName).
		CombinedOutput(); err != nil {
		_ = out
	}

	_ = exec.Command("git", "worktree", "prune").Run()

	return nil
}

// isGitRepo checks whether workDir is inside a git working tree.
func isGitRepo(workDir string) bool {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = workDir
	return cmd.Run() == nil
}

// sanitizeBranchName makes a string safe to use as a git branch name.
// Git refuses: space, ~, ^, :, ?, *, [, .., \ and control chars.
// We replace aggressively.
func sanitizeBranchName(s string) string {
	if s == "" {
		return "anon"
	}
	bad := []string{" ", "\t", "~", "^", ":", "?", "*", "[", "\\", ".."}
	out := s
	for _, b := range bad {
		out = strings.ReplaceAll(out, b, "-")
	}
	out = strings.TrimLeft(out, "-/")
	if out == "" {
		return "anon"
	}
	return out
}
