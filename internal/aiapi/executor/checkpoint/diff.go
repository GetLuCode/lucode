package checkpoint

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ChangeKind describes what would happen to a file when going from one
// tree to another.
type ChangeKind string

const (
	ChangeAdded    ChangeKind = "added"    // file exists in target, not in source
	ChangeRemoved  ChangeKind = "removed"  // file exists in source, not in target
	ChangeModified ChangeKind = "modified" // file exists in both, content differs
)

// Change is one entry in a diff between two trees.
type Change struct {
	Path     string     `json:"path"`
	Kind     ChangeKind `json:"kind"`
	FromHash string     `json:"from_hash,omitempty"` // empty for added
	ToHash   string     `json:"to_hash,omitempty"`   // empty for removed
	// FromContent and ToContent are filled in lazily by the UI layer if it
	// wants to display a preview. Empty here to keep the diff small.
}

// Diff computes the set of changes between two trees. If toID is empty,
// the target is the current workDir state (computed on the fly).
func (s *Store) Diff(fromID, toID string) ([]Change, error) {
	from, err := s.LoadTree(fromID)
	if err != nil {
		return nil, fmt.Errorf("load from tree: %w", err)
	}

	var to *Tree
	if toID == "" {
		// Current workDir state, computed on the fly (NOT persisted).
		to, err = buildTree(s.storeRoot, s.workDir, s.ignore)
		if err != nil {
			return nil, fmt.Errorf("build current tree: %w", err)
		}
	} else {
		to, err = s.LoadTree(toID)
		if err != nil {
			return nil, fmt.Errorf("load to tree: %w", err)
		}
	}
	return diffTrees(from, to), nil
}

func diffTrees(from, to *Tree) []Change {
	fromMap := from.asMap()
	toMap := to.asMap()
	changes := []Change{}

	// Walk all paths in from. If missing from to → removed. If hash differs → modified.
	allPaths := make([]string, 0, len(fromMap)+len(toMap))
	for p := range fromMap {
		allPaths = append(allPaths, p)
	}
	for p := range toMap {
		if _, ok := fromMap[p]; !ok {
			allPaths = append(allPaths, p)
		}
	}
	sort.Strings(allPaths)

	for _, p := range allPaths {
		fh, fok := fromMap[p]
		th, tok := toMap[p]
		switch {
		case !fok && tok:
			changes = append(changes, Change{Path: p, Kind: ChangeAdded, ToHash: th})
		case fok && !tok:
			changes = append(changes, Change{Path: p, Kind: ChangeRemoved, FromHash: fh})
		case fok && tok && fh != th:
			changes = append(changes, Change{Path: p, Kind: ChangeModified, FromHash: fh, ToHash: th})
		}
	}
	return changes
}

// LoadContent returns the file content for a given (path, hash) pair.
// Used by the UI to show previews of changes. Returns the blob content.
func (s *Store) LoadContent(hash string) ([]byte, error) {
	return readBlob(s.storeRoot, hash)
}

// fileAtPath is a small helper to centralize the "given a tree entry,
// return its on-disk path in the workDir" mapping. Keeps callers from
// sprinkling filepath.Join(workDir, slashPath) everywhere.
func (s *Store) fileAtPath(relSlashPath string) string {
	return filepath.Join(s.workDir, filepath.FromSlash(relSlashPath))
}

// removeEmptyParents walks up the path and removes any empty parent
// directories. Used by Revert so that deleting the last file in a dir
// also cleans up the now-empty dir. Stops at the workDir boundary.
func removeEmptyParents(workDir, leaf string) {
	dir := filepath.Dir(leaf)
	for {
		if dir == workDir || dir == "." || dir == "" || dir == string(filepath.Separator) {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(dir); err != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}
