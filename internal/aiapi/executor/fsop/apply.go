package fsop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ApplyResult is what ApplyBatch returns on success.
type ApplyResult struct {
	NewHead string // sha256 of the post-batch tracked state
	OpsRun  int    // how many ops were actually applied (== len(ops) on success)
}

// ApplyBatch validates, then applies a batch of ops to workDir atomically.
// "Atomically" means: if any op fails (validation OR runtime), the workDir
// is rolled back to its pre-batch state and an error is returned. The model
// sees zero partial edits.
//
// expectedHead, if non-empty, must match the current head. A mismatch means
// the model is acting on a stale read; the call is rejected without any
// changes.
func ApplyBatch(ops []FSOp, workDir string, ignore *IgnoreSet, expectedHead string) (ApplyResult, error) {
	if workDir == "" {
		return ApplyResult{}, fmt.Errorf("no working directory set")
	}

	// 1. Compute current head (used for the expected check AND as the
	//    snapshot baseline so we can detect if anything external changed
	//    while we were applying).
	currentHead, err := ComputeHead(workDir, ignore)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("compute head: %w", err)
	}
	if expectedHead != "" && expectedHead != currentHead {
		return ApplyResult{}, fmt.Errorf("stale head checkpoint: expected %s, current is %s", expectedHead, currentHead)
	}

	// 2. Validate + resolve paths for every op up front. Any failure here
	//    means we never touch the filesystem.
	resolved := make([]FSOp, len(ops))
	for i, op := range ops {
		if err := op.Validate(); err != nil {
			return ApplyResult{}, fmt.Errorf("op %d: %w", i, err)
		}
		r, err := op.ResolvePaths(workDir)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("op %d: %w", i, err)
		}
		resolved[i] = r
	}

	// 3. Snapshot every file any op will touch. We capture pre-state so we
	//    can roll back if a later op fails.
	snap := newSnapshot()
	for i, op := range resolved {
		for _, p := range op.affectedPaths() {
			if _, ok := snap.files[p]; !ok {
				if err := snap.capture(p); err != nil {
					return ApplyResult{}, fmt.Errorf("op %d: snapshot %s: %w", i, p, err)
				}
			}
		}
	}

	// 4. Apply ops in order. On first failure, roll back and return.
	for i, op := range resolved {
		if err := applyOne(op); err != nil {
			if rbErr := snap.restore(); rbErr != nil {
				return ApplyResult{}, fmt.Errorf("op %d (%s) failed: %w; rollback also failed: %v", i, op.Op, err, rbErr)
			}
			return ApplyResult{}, fmt.Errorf("op %d (%s) failed: %w; rolled back", i, op.Op, err)
		}
	}

	// 5. Compute new head. If this fails (e.g. workDir got deleted under
	//    us), try to roll back; the model still gets a clean error.
	newHead, err := ComputeHead(workDir, ignore)
	if err != nil {
		_ = snap.restore()
		return ApplyResult{}, fmt.Errorf("compute new head: %w; rolled back", err)
	}

	return ApplyResult{NewHead: newHead, OpsRun: len(resolved)}, nil
}

// applyOne dispatches to the per-op implementation.
func applyOne(op FSOp) error {
	switch op.Op {
	case OpCreate:
		return applyCreate(op)
	case OpUpdate:
		return applyUpdate(op)
	case OpDelete:
		return applyDelete(op)
	case OpRename:
		return applyRename(op)
	case OpCopy:
		return applyCopy(op)
	default:
		// Validate() should have caught this; defensive.
		return fmt.Errorf("unknown op %q", op.Op)
	}
}

func applyCreate(op FSOp) error {
	if _, err := os.Stat(op.Path); err == nil {
		return fmt.Errorf("file already exists at %s", op.Path)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(op.Path), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.WriteFile(op.Path, []byte(op.Content), 0644)
}

func applyUpdate(op FSOp) error {
	data, err := os.ReadFile(op.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("file does not exist at %s (use create to make a new file)", op.Path)
		}
		return err
	}
	original := string(data)
	count := strings.Count(original, op.OldString)
	if count == 0 {
		return fmt.Errorf("old_string not found in %s", op.Path)
	}

	var newContent string
	switch {
	case op.ReplaceAll != nil && *op.ReplaceAll:
		newContent = strings.ReplaceAll(original, op.OldString, op.NewString)
	case op.OccurrenceIndex != nil:
		n := *op.OccurrenceIndex
		if n > count {
			return fmt.Errorf("occurrence_index %d out of range (only %d match(es))", n, count)
		}
		newContent = replaceNth(original, op.OldString, op.NewString, n)
	default:
		if count > 1 {
			return fmt.Errorf("old_string appears %d times in %s (pass replace_all=true to replace every match, or occurrence_index=N for the Nth match)", count, op.Path)
		}
		newContent = strings.Replace(original, op.OldString, op.NewString, 1)
	}

	return os.WriteFile(op.Path, []byte(newContent), 0644)
}

// replaceNth replaces the Nth (1-indexed) occurrence of old in s with new.
// Pre: n >= 1 and strings.Count(s, old) >= n.
func replaceNth(s, old, new string, n int) string {
	idx := 0
	for i := 1; i <= n; i++ {
		pos := strings.Index(s[idx:], old)
		if pos < 0 {
			return s // shouldn't happen if Count was checked
		}
		abs := idx + pos
		if i == n {
			return s[:abs] + new + s[abs+len(old):]
		}
		idx = abs + len(old)
	}
	return s
}

func applyDelete(op FSOp) error {
	if _, err := os.Stat(op.Path); os.IsNotExist(err) {
		return fmt.Errorf("file does not exist at %s", op.Path)
	} else if err != nil {
		return err
	}
	return os.Remove(op.Path)
}

func applyRename(op FSOp) error {
	if _, err := os.Stat(op.From); os.IsNotExist(err) {
		return fmt.Errorf("source does not exist at %s", op.From)
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(op.To); err == nil {
		return fmt.Errorf("destination already exists at %s", op.To)
	}
	if err := os.MkdirAll(filepath.Dir(op.To), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	return os.Rename(op.From, op.To)
}

func applyCopy(op FSOp) error {
	if _, err := os.Stat(op.From); os.IsNotExist(err) {
		return fmt.Errorf("source does not exist at %s", op.From)
	} else if err != nil {
		return err
	}
	if _, err := os.Stat(op.To); err == nil {
		return fmt.Errorf("destination already exists at %s", op.To)
	}
	if err := os.MkdirAll(filepath.Dir(op.To), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}
	data, err := os.ReadFile(op.From)
	if err != nil {
		return err
	}
	return os.WriteFile(op.To, data, 0644)
}

// --- snapshot ---

// snapshotEntry captures the pre-batch state of a single file. existed=false
// means the file was absent; on restore, we delete it. existed=true means we
// have the bytes and mode to write back.
type snapshotEntry struct {
	existed bool
	data    []byte
	mode    os.FileMode
}

type snapshot struct {
	files map[string]snapshotEntry
}

func newSnapshot() *snapshot {
	return &snapshot{files: make(map[string]snapshotEntry)}
}

func (s *snapshot) capture(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		s.files[path] = snapshotEntry{existed: false}
		return nil
	}
	if err != nil {
		return err
	}
	// Skip non-regular files: directories (handled by affectedPaths), symlinks
	// (we don't restore them — too easy to get wrong), devices, etc.
	if !info.Mode().IsRegular() {
		s.files[path] = snapshotEntry{existed: true} // marker only; restore() will leave it
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s.files[path] = snapshotEntry{
		existed: true,
		data:    data,
		mode:    info.Mode().Perm(),
	}
	return nil
}

func (s *snapshot) restore() error {
	var firstErr error
	for path, entry := range s.files {
		var err error
		switch {
		case !entry.existed:
			err = os.Remove(path)
			if err != nil && !os.IsNotExist(err) {
				firstErr = err
			}
		case entry.data == nil:
			// Non-regular file we didn't snapshot content for; skip.
		default:
			if dirErr := os.MkdirAll(filepath.Dir(path), 0755); dirErr != nil {
				firstErr = dirErr
				continue
			}
			if wErr := os.WriteFile(path, entry.data, entry.mode); wErr != nil {
				firstErr = wErr
			}
		}
	}
	return firstErr
}
