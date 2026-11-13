package checkpoint

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// FileDiff is the result of computing a unified diff for one file. It's
// what the lucode UI renders in the diff modal.
type FileDiff struct {
	Path         string `json:"path"`
	FromHash     string `json:"from_hash,omitempty"` // empty for added
	ToHash       string `json:"to_hash,omitempty"`   // empty for removed
	Kind         ChangeKind `json:"kind"`
	UnifiedDiff  string `json:"unified_diff"`        // empty if identical or binary
	IsBinary     bool   `json:"is_binary"`            // true → UnifiedDiff empty
	FromContent  string `json:"from_content,omitempty"`
	ToContent    string `json:"to_content,omitempty"`
	FromLines    int    `json:"from_lines"`
	ToLines      int    `json:"to_lines"`
}

// DiffFile computes the unified diff for a single path between two blobs.
// atFrom and atTo are the blob hashes in the from and to trees (either can
// be empty for added/removed files).
//
// Binary detection: if either side contains a NUL byte in the first 8 KB
// it's treated as binary and the diff is empty (we don't try to do textual
// diffs on binaries).
//
// Returns an error if a non-empty hash can't be loaded (corrupt store, or
// wrong workDir).
func (s *Store) DiffFile(path, hashFrom, hashTo string) (FileDiff, error) {
	fd := FileDiff{Path: path, FromHash: hashFrom, ToHash: hashTo}

	var fromData, toData []byte
	var err error

	if hashFrom != "" {
		fromData, err = readBlob(s.storeRoot, hashFrom)
		if err != nil {
			return fd, fmt.Errorf("read from blob: %w", err)
		}
	}
	if hashTo != "" {
		toData, err = readBlob(s.storeRoot, hashTo)
		if err != nil {
			return fd, fmt.Errorf("read to blob: %w", err)
		}
	}

	// Determine kind.
	switch {
	case hashFrom == "" && hashTo != "":
		fd.Kind = ChangeAdded
	case hashFrom != "" && hashTo == "":
		fd.Kind = ChangeRemoved
	case hashFrom == hashTo:
		fd.Kind = ChangeModified // unusual — caller passed equal hashes
		fd.UnifiedDiff = ""
		return fd, nil
	default:
		fd.Kind = ChangeModified
	}

	// Binary check: NUL byte in first 8 KB of either side.
	const binaryProbe = 8 * 1024
	if hasNUL(fromData, binaryProbe) || hasNUL(toData, binaryProbe) {
		fd.IsBinary = true
		fd.FromContent = string(fromData)
		fd.ToContent = string(toData)
		fd.FromLines = countLines(fromData)
		fd.ToLines = countLines(toData)
		return fd, nil
	}

	// From-content and to-content for the UI (capped at 256 KB to avoid
	// shipping huge files in JSON; the UI can request raw blobs if it
	// needs more).
	const contentCap = 256 * 1024
	if len(fromData) <= contentCap {
		fd.FromContent = string(fromData)
	}
	if len(toData) <= contentCap {
		fd.ToContent = string(toData)
	}
	fd.FromLines = countLines(fromData)
	fd.ToLines = countLines(toData)

	// For added/removed files, the unified diff is "all lines added" /
	// "all lines removed". For modified, it's a real diff.
	var udiff string
	switch fd.Kind {
	case ChangeAdded:
		udiff = buildFullDiff("", path, string(toData))
	case ChangeRemoved:
		udiff = buildFullDiff(string(fromData), path, "")
	case ChangeModified:
		udiff = buildUnifiedDiff(string(fromData), string(toData), path)
	}
	fd.UnifiedDiff = udiff

	return fd, nil
}

// buildUnifiedDiff produces a unified diff (--- a/file +++ b/file @@ ...) for
// text content. Empty input on either side returns empty string.
func buildUnifiedDiff(from, to, path string) string {
	if from == to {
		return ""
	}
	ud := difflib.UnifiedDiff{
		A:        splitLinesPreserve(from),
		B:        splitLinesPreserve(to),
		FromFile: "a/" + path,
		ToFile:   "b/" + path,
		FromDate: "",
		ToDate:   "",
		Context:  3,
	}
	s, err := difflib.GetUnifiedDiffString(ud)
	if err != nil {
		// difflib only errors on Context<0, which we control.
		return ""
	}
	return s
}

// buildFullDiff is used for added/removed files: every line of the present
// side is shown as added/removed. For added: from is "", to is the full
// content. For removed: from is the full content, to is "".
func buildFullDiff(from, path, to string) string {
	if from == "" && to == "" {
		return ""
	}
	ud := difflib.UnifiedDiff{
		A:        splitLinesPreserve(from),
		B:        splitLinesPreserve(to),
		FromFile: "a/" + path,
		ToFile:   "b/" + path,
		Context:  3,
	}
	s, err := difflib.GetUnifiedDiffString(ud)
	if err != nil {
		return ""
	}
	return s
}

// splitLinesPreserve splits s on '\n' and keeps a trailing empty element
// if s ends with '\n' (matches difflib's expectation that each line ends
// with '\n' — difflib's Sequence ignores the trailing newline).
func splitLinesPreserve(s string) []string {
	if s == "" {
		return []string{}
	}
	// difflib treats each element as one line. SplitLines drops the
	// trailing empty if the string ends with \n, which is what we want.
	return strings.Split(s, "\n")
}

func hasNUL(data []byte, probe int) bool {
	if len(data) == 0 {
		return false
	}
	if len(data) > probe {
		data = data[:probe]
	}
	return bytes.IndexByte(data, 0) >= 0
}

func countLines(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	return bytes.Count(data, []byte("\n"))
}
