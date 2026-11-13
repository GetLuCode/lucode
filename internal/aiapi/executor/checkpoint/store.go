package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"claude-orchestrator/internal/aiapi/executor/fsop"
)

// Store is a content-addressable checkpoint store scoped to one workDir.
// It is safe to call Snapshot / Revert / List concurrently from different
// goroutines, but assumes the underlying workDir is only being mutated
// through this store (or atomically through fsop.ApplyBatch). External
// mutations (user editing in their editor, run_command) will show up on
// the next Snapshot as a new tree, and the head will diverge from the
// index — that's expected and the Store surfaces it via GetDiff.
type Store struct {
	storeRoot string // <workDir>/.lucode/checkpoints
	workDir   string

	// ignore is consulted during Snapshot to skip ignored paths (so a
	// snapshot of node_modules never makes it into the index). Revert does
	// NOT touch ignored paths.
	ignore *fsop.IgnoreSet

	// policy controls how aggressive prune is. See PrunePolicy.
	policy PrunePolicy

	// mu protects index, but does NOT serialize Snapshot/Apply against
	// each other. The caller is expected to do that (the executor's
	// agentic loop is sequential).
	mu    sync.Mutex
	index *indexState
}

// NewStore opens the store for workDir with the default PrunePolicy.
// Returns an error only if the filesystem is broken; "first time" is not
// an error.
func NewStore(workDir string) (*Store, error) {
	return NewStoreWithPolicy(workDir, DefaultPrunePolicy())
}

// NewStoreWithPolicy is the explicit form. Use this when you want to
// tune the prune policy (e.g., a CI workspace might want unlimited).
func NewStoreWithPolicy(workDir string, policy PrunePolicy) (*Store, error) {
	if workDir == "" {
		return nil, fmt.Errorf("checkpoint: workDir is empty")
	}
	storeRoot := filepath.Join(workDir, ".lucode", "checkpoints")
	if err := os.MkdirAll(filepath.Join(storeRoot, blobDir), 0755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(storeRoot, treeDir), 0755); err != nil {
		return nil, err
	}
	ignore, err := fsop.NewIgnoreSet(workDir)
	if err != nil {
		return nil, fmt.Errorf("load ignore set: %w", err)
	}
	idx, err := loadIndex(storeRoot)
	if err != nil {
		return nil, err
	}
	return &Store{
		storeRoot: storeRoot,
		workDir:   workDir,
		ignore:    ignore,
		policy:    policy,
		index:     idx,
	}, nil
}

// StoreRoot returns the on-disk directory the store reads/writes. Useful
// for debugging; do not mutate it from outside the package.
func (s *Store) StoreRoot() string { return s.storeRoot }

// Snapshot captures the current state of the workDir, persists the blobs
// and tree, and appends a Checkpoint to the index. The returned Checkpoint
// is the one just appended.
//
// turnID is optional (pass "" if there's no turn concept at this layer).
// ops may be nil (e.g. for an explicit "checkpoint now" with no changes).
// summary should be a short human-readable string, e.g.
// "update foo.go + create bar.go + delete baz.go".
func (s *Store) Snapshot(turnID string, ops []fsop.FSOp, summary string) (Checkpoint, error) {
	tree, err := buildTree(s.storeRoot, s.workDir, s.ignore)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("build tree: %w", err)
	}
	treeHash, err := writeTree(s.storeRoot, tree)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("write tree: %w", err)
	}

	s.mu.Lock()
	parent := s.index.Index.Head
	s.mu.Unlock()

	cp := Checkpoint{
		ID:        treeHash,
		Parent:    parent,
		Tree:      treeHash,
		Timestamp: time.Now().UTC(),
		TurnID:    turnID,
		Summary:   summary,
		Ops:       ops,
		Kind:      "turn",
	}

	if err := s.index.append(cp); err != nil {
		return Checkpoint{}, fmt.Errorf("append checkpoint: %w", err)
	}

	// Best-effort prune. A failure here shouldn't fail the Snapshot — the
	// user just has a longer history than the policy allows for one more
	// turn. Next Snapshot will retry.
	s.prune()

	return cp, nil
}

// Head returns the tree hash of the most recent checkpoint in the index.
// Note: this can be stale if the workDir has been mutated since the last
// Snapshot (use fsop.ComputeHead to get the on-disk truth).
func (s *Store) Head() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.index.Index.Head
}

// List returns all checkpoints in chronological order (oldest first).
// The slice is a copy — safe to mutate.
func (s *Store) List() []Checkpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Checkpoint, len(s.index.Index.Checkpoints))
	copy(out, s.index.Index.Checkpoints)
	return out
}

// Get returns a single checkpoint by ID. Returns an error if not found.
func (s *Store) Get(id string) (Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cp := range s.index.Index.Checkpoints {
		if cp.ID == id || cp.Tree == id {
			return cp, nil
		}
	}
	return Checkpoint{}, fmt.Errorf("checkpoint %s not found", id)
}

// LoadTree returns the Tree object for a given checkpoint id. Useful for
// the UI to display a checkpoint's contents without re-walking the disk.
func (s *Store) LoadTree(id string) (*Tree, error) {
	cp, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	return loadTree(s.storeRoot, cp.Tree)
}
