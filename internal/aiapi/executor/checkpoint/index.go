package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"claude-orchestrator/internal/aiapi/executor/fsop"
)

// Checkpoint is one entry in the linear history. ID and Tree are the same
// value (kept separate for readability in the index.json).
type Checkpoint struct {
	ID        string      `json:"id"`        // = Tree (kept for UI readability)
	Parent    string      `json:"parent"`    // previous checkpoint's tree hash
	Tree      string      `json:"tree"`      // this checkpoint's tree hash
	Timestamp time.Time   `json:"timestamp"`
	TurnID    string      `json:"turn_id,omitempty"`
	Summary   string      `json:"summary"`             // human-readable one-liner
	Ops       []fsop.FSOp `json:"ops,omitempty"`       // what was applied (empty for revert)
	Kind      string      `json:"kind,omitempty"`      // "turn" (default) or "revert"
	Pruned    bool        `json:"pruned,omitempty"`    // true = tree object was deleted; can't revert
}

// Index is the on-disk index.json — a linear log of checkpoints in
// chronological order. The most recent entry's Tree is the store's head.
type Index struct {
	Version     int          `json:"version"`
	Head        string       `json:"head"`     // tree hash of the most recent state
	Checkpoints []Checkpoint `json:"checkpoints"`
}

// indexState wraps an in-memory Index with disk persistence. All mutations
// go through it so the on-disk file and the in-memory copy never diverge.
type indexState struct {
	mu    sync.Mutex
	path  string // full path to index.json
	Index Index
}

// loadIndex reads index.json from disk. If the file doesn't exist, returns
// an empty Index. Disk I/O errors propagate.
func loadIndex(storeRoot string) (*indexState, error) {
	state := &indexState{
		path:  filepath.Join(storeRoot, indexFile),
		Index: Index{Version: 1, Checkpoints: []Checkpoint{}},
	}
	data, err := os.ReadFile(state.path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &state.Index); err != nil {
		return nil, fmt.Errorf("parse %s: %w", state.path, err)
	}
	if state.Index.Checkpoints == nil {
		state.Index.Checkpoints = []Checkpoint{}
	}
	return state, nil
}

// save writes the index back to disk atomically (temp file + rename).
// Caller must hold the mutex.
func (s *indexState) save() error {
	data, err := json.MarshalIndent(s.Index, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// append adds a checkpoint to the end of the log and updates the head.
// Atomic with a save().
func (s *indexState) append(cp Checkpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Index.Checkpoints = append(s.Index.Checkpoints, cp)
	s.Index.Head = cp.Tree
	return s.save()
}
