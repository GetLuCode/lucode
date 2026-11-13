// Package checkpoint implements a content-addressable checkpoint store
// layered on top of fsop. Each successful batch of filesystem operations
// produces a checkpoint (a snapshot of the workDir's tracked file state),
// and the user can revert the workDir to any previous checkpoint.
//
// Storage layout (inside the workDir, hidden from the user):
//
//	.lucode/checkpoints/
//	  index.json           linear log: [{id, parent, tree, ts, turn_id, summary, ops}, ...]
//	  blobs/<hash>         file content, addressed by sha256
//	  trees/<hash>         tree object, JSON: {"entries":[{"path","hash"}], ...}
//
// Content-addressable means: the filename of a blob/tree IS the sha256 of
// its content, so two checkpoints that share an unchanged file share a
// single blob. Storage cost is O(changed files per checkpoint), not O(files
// × checkpoints).
//
// Concurrent access is not supported in v1 — the store assumes a single
// agent. Multi-agent use will need a mutex around Snapshot/Revert; that's
// a TODO, not a v1 feature.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// blobDir is the subdirectory name (relative to the store root) where
// content-addressable blobs live.
const blobDir = "blobs"

// treeDir is the subdirectory name for tree objects.
const treeDir = "trees"

// indexFile is the filename of the linear index.
const indexFile = "index.json"

// HashContent returns the hex sha256 of content. The hash is also used as
// the on-disk filename for the blob, so equality on hash is equality on
// content.
func HashContent(content []byte) string {
	h := sha256.Sum256(content)
	return hex.EncodeToString(h[:])
}

// writeBlob persists content under <storeRoot>/blobs/<hash>. If the blob
// already exists (concurrent write or repeated snapshot of unchanged file),
// this is a no-op — content-addressable storage means collisions are safe.
func writeBlob(storeRoot string, content []byte) (string, error) {
	hash := HashContent(content)
	path := blobPath(storeRoot, hash)
	if _, err := os.Stat(path); err == nil {
		return hash, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	// Atomic write: temp + rename. Renames are atomic on POSIX and on NTFS.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, content, 0644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return hash, nil
}

// readBlob returns the content for a blob hash, or an error if the blob
// doesn't exist on disk (corrupt index or truncated store).
func readBlob(storeRoot, hash string) ([]byte, error) {
	path := blobPath(storeRoot, hash)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("blob %s: %w", hash, err)
	}
	defer f.Close()
	return io.ReadAll(f)
}

func blobPath(storeRoot, hash string) string {
	// Two-level fanout avoids huge directories on big projects.
	// <root>/blobs/aa/bb/aabbcc... — first 4 hex chars as subdirs.
	if len(hash) < 4 {
		return filepath.Join(storeRoot, blobDir, hash)
	}
	return filepath.Join(storeRoot, blobDir, hash[:2], hash[2:4], hash)
}

func treePath(storeRoot, hash string) string {
	if len(hash) < 4 {
		return filepath.Join(storeRoot, treeDir, hash)
	}
	return filepath.Join(storeRoot, treeDir, hash[:2], hash[2:4], hash)
}
