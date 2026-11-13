package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Revert restores the workDir to the state at the given checkpoint id.
// The workDir's tracked file state will exactly match the target tree
// (modulo ignored paths, which are not touched by Revert).
//
// Revert is itself a tracked action: a new "revert" checkpoint is appended
// to the index with the post-revert state. So you can "undo a revert" by
// reverting to the checkpoint just before the revert.
//
// Idempotency: if the workDir already matches the target tree, Revert
// still creates a checkpoint (so the history shows the user explicitly
// confirmed the state), but the filesystem is left untouched.
func (s *Store) Revert(checkpointID string) (Checkpoint, error) {
	target, err := s.LoadTree(checkpointID)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("load target: %w", err)
	}

	// Compute current state to determine the diff. We don't use buildTree
	// here because we want to delete files that are in current but not in
	// target — we need the full current file set, including its on-disk
	// content (we'll be deleting by path, not by hash).
	currentTree, err := buildTree(s.storeRoot, s.workDir, s.ignore)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("build current tree: %w", err)
	}

	changes := diffTrees(currentTree, target)

	// Apply in a safe order: deletes first (so we don't delete a parent
	// before its children), then creates, then modifications. Actually for
	// safety, do it in this order:
	//   1. Modifications: write the new content to a temp file, then
	//      rename. Renames overwrite the destination atomically.
	//   2. Adds: same pattern, temp + rename.
	//   3. Removes: delete the file (after any modifications that might
	//      have rewritten it).
	// If any step fails, we want to roll back. For v1, we don't roll back
	// Revert — the user can re-revert to a more recent checkpoint if
	// something went wrong. The risk is low because Revert only touches
	// tracked files.
	//
	// We process changes in sorted order to make Revert deterministic.
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })

	for _, ch := range changes {
		switch ch.Kind {
		case ChangeModified, ChangeAdded:
			absPath := s.fileAtPath(ch.Path)
			content, err := readBlob(s.storeRoot, ch.ToHash)
			if err != nil {
				return Checkpoint{}, fmt.Errorf("read blob %s for %s: %w", ch.ToHash, ch.Path, err)
			}
			if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
				return Checkpoint{}, fmt.Errorf("mkdir for %s: %w", ch.Path, err)
			}
			// Write atomically. If the file exists, rename overwrites it on
			// POSIX and on NTFS.
			tmp := absPath + ".lucode-tmp"
			if err := os.WriteFile(tmp, content, 0644); err != nil {
				return Checkpoint{}, fmt.Errorf("write %s: %w", ch.Path, err)
			}
			if err := os.Rename(tmp, absPath); err != nil {
				os.Remove(tmp)
				return Checkpoint{}, fmt.Errorf("rename %s: %w", ch.Path, err)
			}
		}
	}
	for _, ch := range changes {
		if ch.Kind != ChangeRemoved {
			continue
		}
		absPath := s.fileAtPath(ch.Path)
		if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
			return Checkpoint{}, fmt.Errorf("remove %s: %w", ch.Path, err)
		}
		// Clean up empty parent dirs so we don't leave `src/` behind when
		// the user deletes the last file in it.
		removeEmptyParents(s.workDir, absPath)
	}

	// Re-snapshot the post-revert state and append as a "revert" checkpoint.
	// The new tree hash should match the target hash (because we restored
	// exactly the target's files), but we recompute it from disk to be sure
	// — and so the timestamp is fresh.
	tree, err := buildTree(s.storeRoot, s.workDir, s.ignore)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("post-revert build tree: %w", err)
	}
	newTreeHash, err := writeTree(s.storeRoot, tree)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("post-revert write tree: %w", err)
	}

	s.mu.Lock()
	parent := s.index.Index.Head
	s.mu.Unlock()

	cp := Checkpoint{
		ID:        newTreeHash,
		Parent:    parent,
		Tree:      newTreeHash,
		Timestamp: time.Now().UTC(),
		Summary:   fmt.Sprintf("revert to %s", checkpointID),
		Kind:      "revert",
	}
	if err := s.index.append(cp); err != nil {
		return Checkpoint{}, fmt.Errorf("append revert checkpoint: %w", err)
	}
	return cp, nil
}
