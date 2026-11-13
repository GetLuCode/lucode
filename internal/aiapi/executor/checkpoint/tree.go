package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Tree is a snapshot of a workDir's tracked file state: a sorted list of
// (path, blob_hash) pairs. Two trees with the same entries have the same
// content hash, so tree equality is just hash equality.
//
// Entries are sorted by path to make the hash deterministic regardless of
// the walk order from the underlying filesystem.
type Tree struct {
	Entries []TreeEntry `json:"entries"`
}

// TreeEntry is one (path, blob_hash) pair in a tree.
type TreeEntry struct {
	Path string `json:"path"`
	Hash string `json:"hash"`
}

// buildTree walks the workDir, hashes the content of every tracked file
// (per the ignore set), persists each blob, and returns the resulting Tree.
// The tree is NOT persisted by this function — writeTree does that.
//
// The ignore set is consulted for every path. Symlinks are skipped to
// avoid cycles. Non-regular files (devices, fifos) are skipped.
func buildTree(storeRoot, workDir string, ignore interface {
	IsIgnored(relPath string) bool
}) (*Tree, error) {
	entries := []TreeEntry{}

	err := filepath.WalkDir(workDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == workDir {
			return nil
		}
		// Skip symlinks (don't follow, don't record).
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(workDir, path)
		if err != nil {
			return err
		}
		relSlash := filepath.ToSlash(rel)
		if ignore != nil && ignore.IsIgnored(relSlash) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// Read content, hash it, persist as a blob.
		data, err := os.ReadFile(path)
		if err != nil {
			// File vanished mid-walk; skip silently.
			return nil
		}
		hash, err := writeBlob(storeRoot, data)
		if err != nil {
			return fmt.Errorf("write blob for %s: %w", relSlash, err)
		}
		entries = append(entries, TreeEntry{Path: relSlash, Hash: hash})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})
	return &Tree{Entries: entries}, nil
}

// Hash returns the sha256 of the tree's content. Equal trees have equal
// hashes. The tree is the "value" that this hash represents.
func (t *Tree) Hash() (string, error) {
	if t == nil {
		return "", fmt.Errorf("nil tree")
	}
	// Re-serialize to make sure the order is canonical (defensive — buildTree
	// already sorts, but if someone hand-constructs a Tree the hash would
	// otherwise be order-dependent).
	canonical := *t
	sort.Slice(canonical.Entries, func(i, j int) bool {
		return canonical.Entries[i].Path < canonical.Entries[j].Path
	})
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), nil
}

// writeTree persists the tree under <storeRoot>/trees/<hash>. Like writeBlob,
// this is a no-op if the file already exists.
func writeTree(storeRoot string, t *Tree) (string, error) {
	hash, err := t.Hash()
	if err != nil {
		return "", err
	}
	path := treePath(storeRoot, hash)
	if _, err := os.Stat(path); err == nil {
		return hash, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return hash, nil
}

// loadTree reads a tree object from disk. Returns an error if the hash
// doesn't exist (corrupt index or pruned store).
func loadTree(storeRoot, hash string) (*Tree, error) {
	path := treePath(storeRoot, hash)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load tree %s: %w", hash, err)
	}
	var t Tree
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parse tree %s: %w", hash, err)
	}
	return &t, nil
}

// asMap returns the tree as a path → hash map for fast lookups.
func (t *Tree) asMap() map[string]string {
	m := make(map[string]string, len(t.Entries))
	for _, e := range t.Entries {
		m[e.Path] = e.Hash
	}
	return m
}
