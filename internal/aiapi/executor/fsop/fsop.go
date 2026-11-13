// Package fsop implements the batched filesystem operations used by the
// filesystem tool. It owns the op shape, validation, path resolution, ignore
// patterns, head computation, and atomic apply-with-rollback.
//
// Design goals:
//   - One tool, many ops: the wire format is a single JSON object with an
//     ops[] array. Each op has a discriminator ("op") and an op-specific
//     shape. See the schema in internal/executor/tools.go.
//   - Atomic: if any op in a batch fails, NONE of the ops are applied.
//   - Revertable: every ApplyBatch call returns a head id (sha256 of the
//     tracked file state). The model can pass this back via
//     expected_head_checkpoint to catch stale reads.
package fsop

import (
	"fmt"
	"strings"
)

// Op kinds. The model emits these as the "op" field; keep them in sync with
// the schema's enum in tools.go.
const (
	OpCreate = "create"
	OpUpdate = "update"
	OpDelete = "delete"
	OpRename = "rename"
	OpCopy   = "copy"
)

// FSOp is one operation in a batch. Fields are pointers (where it matters)
// so we can distinguish "not provided" from "provided as zero value":
//   - ReplaceAll *bool:  nil → default false, *false → explicit false, *true → true
//   - OccurrenceIndex *int: nil → no Nth-match selector, *N → replace the Nth (1-indexed)
type FSOp struct {
	Op              string `json:"op"`
	Path            string `json:"path,omitempty"`
	Content         string `json:"content,omitempty"`
	OldString       string `json:"old_string,omitempty"`
	NewString       string `json:"new_string,omitempty"`
	ReplaceAll      *bool  `json:"replace_all,omitempty"`
	OccurrenceIndex *int   `json:"occurrence_index,omitempty"`
	From            string `json:"from,omitempty"`
	To              string `json:"to,omitempty"`
}

// Validate checks that the op has the right shape and the required fields
// for its op kind. It does NOT touch the filesystem.
func (o FSOp) Validate() error {
	switch o.Op {
	case OpCreate:
		if strings.TrimSpace(o.Path) == "" {
			return fmt.Errorf("create: path is required")
		}
		// Content is allowed to be the empty string (legitimate empty file).
		// We can't distinguish "missing" from "" with this struct, but
		// additionalProperties:false on the schema side catches stray fields.
	case OpUpdate:
		if strings.TrimSpace(o.Path) == "" {
			return fmt.Errorf("update: path is required")
		}
		if o.OldString == "" {
			return fmt.Errorf("update: old_string is required (pass new_string=\"\" for a no-op edit)")
		}
		if o.OccurrenceIndex != nil && *o.OccurrenceIndex < 1 {
			return fmt.Errorf("update: occurrence_index must be >= 1")
		}
	case OpDelete:
		if strings.TrimSpace(o.Path) == "" {
			return fmt.Errorf("delete: path is required")
		}
	case OpRename, OpCopy:
		if strings.TrimSpace(o.From) == "" {
			return fmt.Errorf("%s: from is required", o.Op)
		}
		if strings.TrimSpace(o.To) == "" {
			return fmt.Errorf("%s: to is required", o.Op)
		}
		if o.From == o.To {
			return fmt.Errorf("%s: from and to must differ", o.Op)
		}
	case "":
		return fmt.Errorf("op is required (one of: create, update, delete, rename, copy)")
	default:
		return fmt.Errorf("unknown op %q (one of: create, update, delete, rename, copy)", o.Op)
	}
	return nil
}

// ResolvePaths returns a copy of the op with all relative paths (Path, From,
// To) resolved against workDir and verified to be inside it. Path traversal
// (incl. platform-foreign absolute paths like /etc/passwd on Windows) is
// rejected here.
func (o FSOp) ResolvePaths(workDir string) (FSOp, error) {
	out := o
	if o.Path != "" {
		p, err := ResolvePath(o.Path, workDir)
		if err != nil {
			return out, err
		}
		out.Path = p
	}
	if o.From != "" {
		p, err := ResolvePath(o.From, workDir)
		if err != nil {
			return out, err
		}
		out.From = p
	}
	if o.To != "" {
		p, err := ResolvePath(o.To, workDir)
		if err != nil {
			return out, err
		}
		out.To = p
	}
	return out, nil
}

// affectedPaths returns the set of files this op will read or modify, in
// any order. Used to pre-snapshot files for rollback. Files that don't yet
// exist (e.g. the "to" of a rename where the source is new) are skipped.
func (o FSOp) affectedPaths() []string {
	switch o.Op {
	case OpCreate, OpUpdate, OpDelete:
		if o.Path == "" {
			return nil
		}
		return []string{o.Path}
	case OpRename, OpCopy:
		// Both source and destination matter: we may need to restore
		// either if the op fails or the destination was newly created.
		var p []string
		if o.From != "" {
			p = append(p, o.From)
		}
		if o.To != "" {
			p = append(p, o.To)
		}
		return p
	}
	return nil
}
