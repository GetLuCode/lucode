package fsop

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
)

// ComputeHead returns a stable hash of the tracked file state in workDir.
// Used as the "head checkpoint" id returned to the model after a successful
// ApplyBatch and embedded in the response of every read tool so the model
// can pass it back as expected_head_checkpoint.
//
// We hash (path, size, mtime) tuples, not file contents. That's a few
// orders of magnitude faster than hashing all bytes, and the mtime changes
// whenever the file changes — content changes can't happen without an
// mtime change in any reasonable editor. The trade-off: an out-of-band
// `touch` on a file with the same content would falsely look like a change.
// Acceptable for v1; a content-based hash can be added in v2.
//
// Ignored paths (per IgnoreSet) and symlinks are skipped: a snapshot of
// node_modules would dwarf a snapshot of the actual source.
func ComputeHead(workDir string, ignore *IgnoreSet) (string, error) {
	type entry struct {
		path  string
		size  int64
		mtime int64
	}
	var entries []entry

	err := filepath.WalkDir(workDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == workDir {
			return nil
		}
		// Skip symlinks entirely (don't follow, don't record).
		if d.Type()&fs.ModeSymlink != 0 {
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
		info, err := d.Info()
		if err != nil {
			// File vanished mid-walk; treat as "not here" and skip.
			return nil
		}
		entries = append(entries, entry{
			path:  relSlash,
			size:  info.Size(),
			mtime: info.ModTime().UnixNano(),
		})
		return nil
	})
	if err != nil {
		return "", err
	}

	// Sort by path so the hash is deterministic regardless of walk order
	// (which can differ across filesystems and OSes).
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].path < entries[j].path
	})

	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\t%d\t%d\n", e.path, e.size, e.mtime)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
