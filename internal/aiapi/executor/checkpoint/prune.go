package checkpoint

import (
	"log"
	"os"
	"time"
)

// PrunePolicy decides which checkpoints to keep. The default (set by
// DefaultPrunePolicy) keeps the last 200 non-revert checkpoints and any
// checkpoint newer than 30 days. Revert checkpoints are always kept
// because they encode a user action — losing them silently would be
// surprising.
//
// Set any field to zero to disable that constraint. The zero value of
// PrunePolicy is "never prune" — explicit defaults are recommended.
type PrunePolicy struct {
	// MaxCheckpoints is the maximum number of non-revert checkpoints to
	// keep. 0 means no limit.
	MaxCheckpoints int

	// MaxAge drops any checkpoint older than now - MaxAge. 0 means no
	// age-based pruning. Note: this affects reverts too unless
	// KeepReverts is true.
	MaxAge time.Duration

	// KeepReverts, if true, never prunes Kind=="revert" checkpoints.
	// They represent explicit user actions and are cheap to keep.
	KeepReverts bool
}

// DefaultPrunePolicy is the policy used when a Store is opened without an
// explicit one. Values are conservative; tune via the constructor.
func DefaultPrunePolicy() PrunePolicy {
	return PrunePolicy{
		MaxCheckpoints: 200,
		MaxAge:         30 * 24 * time.Hour,
		KeepReverts:    true,
	}
}

// prune is called after every successful Snapshot. It walks the index
// and marks old/excess checkpoints as pruned (deletes their tree object
// from disk; the index entry is preserved with Pruned=true so the UI can
// still display the metadata as historical record).
//
// Blobs are NOT garbage-collected here — a blob may be shared between a
// pruned checkpoint and a kept one. A separate GC pass (v2) walks blobs
// and deletes any not referenced by any kept checkpoint's tree.
func (s *Store) prune() {
	if s.policy.MaxCheckpoints <= 0 && s.policy.MaxAge <= 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cps := s.index.Index.Checkpoints
	now := time.Now()

	// First pass: age-based pruning. Anything older than MaxAge and not a
	// protected revert gets pruned.
	anyChanged := false
	if s.policy.MaxAge > 0 {
		for i := range cps {
			if cps[i].Pruned {
				continue
			}
			if s.policy.KeepReverts && cps[i].Kind == "revert" {
				continue
			}
			if now.Sub(cps[i].Timestamp) > s.policy.MaxAge {
				s.markPruned(i)
				anyChanged = true
			}
		}
	}

	// Second pass: count-based pruning. If we still have more than
	// MaxCheckpoints unpruned non-revert entries, drop the oldest until
	// we're under the limit.
	if s.policy.MaxCheckpoints > 0 {
		keep := 0
		for i := range cps {
			if cps[i].Pruned {
				continue
			}
			if s.policy.KeepReverts && cps[i].Kind == "revert" {
				continue
			}
			keep++
		}
		excess := keep - s.policy.MaxCheckpoints
		if excess > 0 {
			for i := range cps {
				if excess <= 0 {
					break
				}
				if cps[i].Pruned {
					continue
				}
				if s.policy.KeepReverts && cps[i].Kind == "revert" {
					continue
				}
				s.markPruned(i)
				excess--
				anyChanged = true
			}
		}
	}

	if anyChanged {
		if err := s.index.save(); err != nil {
			log.Printf("[checkpoint] prune: save index: %v", err)
		}
	}
}

// markPruned flips the in-memory Pruned flag and removes the tree object
// from disk. Caller must hold s.mu.
func (s *Store) markPruned(i int) {
	cp := &s.index.Index.Checkpoints[i]
	cp.Pruned = true
	treeFile := treePath(s.storeRoot, cp.Tree)
	if err := os.Remove(treeFile); err != nil && !os.IsNotExist(err) {
		log.Printf("[checkpoint] prune: remove tree %s: %v", cp.Tree, err)
	}
}
