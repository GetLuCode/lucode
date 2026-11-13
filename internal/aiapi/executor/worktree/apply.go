package worktree

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"claude-orchestrator/internal/aiapi/executor/fsop"
)

// ApplyResult is what Manager.Apply returns.
type ApplyResult struct {
	// ConflictedFiles are the files that ended up with conflict markers
	// after the 3-way merge. The user will see them as uncommitted
	// modifications until they (or the agent) resolve them.
	ConflictedFiles []string

	// CleanFiles are the files that merged without conflict. They are
	// now in the user's workDir as uncommitted modifications.
	CleanFiles []string

	// DeletedFiles are files that existed in HEAD but were deleted by
	// the model in the worktree. They were also deleted from the user's
	// workDir (assuming the user hadn't modified them since HEAD).
	DeletedFiles []string
}

// Apply performs the 3-way merge from the worktree to the user's workDir.
// For each file that the model changed in the worktree, we look at the
// three versions (HEAD, user, model) and merge them:
//
//	- user == HEAD: user didn't change it → take model's version
//	- model == HEAD: model didn't change it → leave it alone
//	- both changed: run `git merge-file -p`; on conflict, the file
//	  gets `<<<<<<<` / `=======` / `>>>>>>>` markers and the
//	  conflict is reported
//
// The result is that the user's workDir now reflects the model's
// changes (plus the user's uncommitted changes, where they don't
// conflict). Conflict files need a follow-up resolution turn where
// the model cleans up the markers.
//
// In passthrough mode (no git), Apply is a no-op — there was no
// worktree to apply from.
func (m *Manager) Apply() (*ApplyResult, error) {
	m.mu.Lock()
	if !m.enabled || m.worktreePath == "" {
		m.mu.Unlock()
		return &ApplyResult{}, nil
	}
	m.mu.Unlock()

	// Find files the model changed (worktree vs HEAD).
	changed, err := m.listChangedFiles()
	if err != nil {
		return nil, err
	}
	if len(changed) == 0 {
		return &ApplyResult{}, nil
	}

	ignore, _ := fsop.NewIgnoreSet(m.userWorkDir)

	result := &ApplyResult{}
	for _, rel := range changed {
		if ignore != nil && ignore.IsIgnored(rel) {
			continue
		}
		conflicted, deleted, err := m.mergeOne(rel)
		if err != nil {
			return nil, fmt.Errorf("merge %s: %w", rel, err)
		}
		switch {
		case deleted:
			result.DeletedFiles = append(result.DeletedFiles, rel)
		case conflicted:
			result.ConflictedFiles = append(result.ConflictedFiles, rel)
		default:
			result.CleanFiles = append(result.CleanFiles, rel)
		}
	}
	return result, nil
}

// listChangedFiles returns the files that differ between the worktree
// and HEAD (the model's changes, including adds and deletes). We stage
// everything first so untracked files appear in the diff.
func (m *Manager) listChangedFiles() ([]string, error) {
	worktreePath := m.Path()
	// Stage all changes (modified, untracked, deleted) so the diff
	// against HEAD picks them up. We don't commit — the index just
	// reflects the working tree.
	add := exec.Command("git", "add", "-A")
	add.Dir = worktreePath
	if out, err := add.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git add -A in worktree: %s: %w", strings.TrimSpace(string(out)), err)
	}
	cmd := exec.Command("git", "diff", "--cached", "--name-only", "HEAD")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git diff in worktree: %w", err)
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return nil, nil
	}
	return strings.Split(s, "\n"), nil
}

// readGitHead reads the file content as of HEAD. Returns os.ErrNotExist
// if the file was added by the model (i.e. doesn't exist in HEAD).
func (m *Manager) readGitHead(relPath string) ([]byte, bool, error) {
	cmd := exec.Command("git", "show", "HEAD:"+relPath)
	cmd.Dir = m.userWorkDir
	out, err := cmd.Output()
	if err != nil {
		// "fatal: Path '...' exists on disk, but not in 'HEAD'" — file is added
		if exitErr, ok := err.(*exec.ExitError); ok {
			msg := strings.TrimSpace(string(exitErr.Stderr))
			if strings.Contains(msg, "not in 'HEAD'") || strings.Contains(msg, "does not exist") {
				return nil, false, nil
			}
		}
		return nil, false, err
	}
	return out, true, nil
}

// mergeOne performs a 3-way merge of one file. The three sides are:
//   - HEAD: the committed state (the "ancestor")
//   - user: the user's current working dir version
//   - model: the worktree version (what the model produced)
//
// Returns:
//   - conflicted=true if the file ends up with conflict markers
//   - deleted=true if the model deleted the file (and there was no user
//     conflict to preserve)
func (m *Manager) mergeOne(relPath string) (conflicted, deleted bool, err error) {
	head, headExists, err := m.readGitHead(relPath)
	if err != nil {
		return false, false, err
	}

	userPath := filepath.Join(m.userWorkDir, relPath)
	userBytes, userErr := os.ReadFile(userPath)
	userExists := userErr == nil
	if userErr != nil && !os.IsNotExist(userErr) {
		return false, false, userErr
	}

	workBytes, workErr := os.ReadFile(filepath.Join(m.worktreePath, relPath))
	workExists := workErr == nil
	if workErr != nil && !os.IsNotExist(workErr) {
		return false, false, workErr
	}

	// CASE 1: model added a new file (not in HEAD, present in worktree).
	if workExists && !headExists {
		// The user may or may not have a file with the same path. If they
		// do and it differs from HEAD... well, it didn't exist in HEAD
		// either, so we have no ancestor. There's no merge to do. We
		// treat any existing user file as a conflict and write markers
		// so the model can decide.
		if userExists && len(userBytes) > 0 {
			// Write a conflict marker file (no ancestor to merge against).
			merged := fmt.Sprintf(
				"<<<<<<< user (no ancestor in HEAD)\n%s\n=======\n%s\n>>>>>>> model\n",
				string(userBytes), string(workBytes))
			if err := os.MkdirAll(filepath.Dir(userPath), 0755); err != nil {
				return false, false, err
			}
			if err := os.WriteFile(userPath, []byte(merged), 0644); err != nil {
				return false, false, err
			}
			return true, false, nil
		}
		// No conflict — just create the file in the user's workDir.
		if err := os.MkdirAll(filepath.Dir(userPath), 0755); err != nil {
			return false, false, err
		}
		return false, false, os.WriteFile(userPath, workBytes, 0644)
	}

	// CASE 2: model deleted a file (in HEAD, missing in worktree).
	if !workExists && headExists {
		// If the user's version is identical to HEAD, just delete.
		if !userExists || string(userBytes) == string(head) {
			if userExists {
				if err := os.Remove(userPath); err != nil && !os.IsNotExist(err) {
					return false, false, err
				}
			}
			return false, true, nil
		}
		// User modified the file. We can't just delete it. Write a
		// conflict marker file so the model can decide.
		merged := fmt.Sprintf(
			"<<<<<<< user\n%s\n=======\n(file deleted by model)\n>>>>>>> model\n",
			string(userBytes))
		if err := os.WriteFile(userPath, []byte(merged), 0644); err != nil {
			return false, false, err
		}
		return true, false, nil
	}

	// CASE 3: file is in both HEAD and worktree (typical edit case).

	// Fast path: user didn't change it (user == HEAD). Just take model.
	if !userExists || string(userBytes) == string(head) {
		if err := os.MkdirAll(filepath.Dir(userPath), 0755); err != nil {
			return false, false, err
		}
		return false, false, os.WriteFile(userPath, workBytes, 0644)
	}

	// Fast path: model didn't change it (model == HEAD). Leave it.
	if string(workBytes) == string(head) {
		return false, false, nil
	}

	// Both changed — 3-way merge with conflict detection via git merge-file.
	return m.runMergeFile(userPath, head, userBytes, workBytes)
}

// runMergeFile invokes `git merge-file -p` on three temp-file inputs and
// writes the result back to userPath. Returns whether the merge had
// conflicts.
func (m *Manager) runMergeFile(userPath string, base, current, other []byte) (conflicted, deleted bool, err error) {
	// Write temp files for the two non-current inputs.
	baseFile, err := writeTemp("merge-base-", base)
	if err != nil {
		return false, false, err
	}
	defer os.Remove(baseFile)

	otherFile, err := writeTemp("merge-other-", other)
	if err != nil {
		return false, false, err
	}
	defer os.Remove(otherFile)

	// If the current file doesn't exist (edge case), create a temp empty one.
	var currentFile string
	if _, statErr := os.Stat(userPath); os.IsNotExist(statErr) {
		currentFile, err = writeTemp("merge-current-", []byte(""))
		if err != nil {
			return false, false, err
		}
		defer os.Remove(currentFile)
	} else {
		currentFile = userPath
	}

	cmd := exec.Command("git", "merge-file", "-p", currentFile, baseFile, otherFile)
	merged, cmdErr := cmd.Output()
	if cmdErr != nil {
		// Non-zero exit means conflict. The merged output is still valid
		// (it includes the conflict markers), so we use it.
		if exitErr, ok := cmdErr.(*exec.ExitError); ok {
			if exitErr.ExitCode() == 0 {
				// git can return 0 even on "conflict" via -p sometimes;
				// rely on marker detection below.
			} else {
				conflicted = true
			}
		} else {
			return false, false, cmdErr
		}
	}

	// git merge-file's exit code semantics: 0 = no conflict, non-zero
	// = conflict. But `-p` may also return 0 when only adding text
	// (no overlap). We do a final marker check to be safe.
	if !conflicted && containsConflictMarker(merged) {
		conflicted = true
	}

	if err := os.MkdirAll(filepath.Dir(userPath), 0755); err != nil {
		return false, false, err
	}
	if err := os.WriteFile(userPath, merged, 0644); err != nil {
		return false, false, err
	}
	return conflicted, false, nil
}

func writeTemp(prefix string, content []byte) (string, error) {
	f, err := os.CreateTemp("", prefix+"*")
	if err != nil {
		return "", err
	}
	if _, err := f.Write(content); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func containsConflictMarker(b []byte) bool {
	return bytes.Contains(b, []byte("<<<<<<<")) ||
		bytes.Contains(b, []byte("=======")) && bytes.Contains(b, []byte(">>>>>>>"))
}
