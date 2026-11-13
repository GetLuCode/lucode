package fsop

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// defaultIgnorePatterns is the conservative set of paths that are NEVER
// checkpointed. These are build outputs, dependencies, VCS, IDE/OS junk, and
// the orchestrator's own state. Source code is never in here.
//
// The format is gitignore-ish: a name without "/" matches a directory OR a
// file with that basename at any depth. A pattern with "/" matches a path
// relative to the workDir. We keep parsing intentionally minimal — full
// gitignore semantics (anchoring, double-star, character classes) can come
// later; this list covers the 95% case.
var defaultIgnorePatterns = []string{
	// Dependencies
	"node_modules",
	"vendor",
	".venv",
	"venv",
	"env",
	// Build outputs
	"dist",
	"build",
	"out",
	"target",
	"bin",
	"obj",
	".next",
	".nuxt",
	".svelte-kit",
	".parcel-cache",
	".turbo",
	// Caches
	".cache",
	"__pycache__",
	".pytest_cache",
	".mypy_cache",
	".ruff_cache",
	// VCS
	".git",
	".svn",
	".hg",
	// IDE / OS
	".idea",
	".vscode",
	".DS_Store",
	"Thumbs.db",
	// Our own store
	".lucode",
	// Coverage
	"coverage",
	".nyc_output",
	"htmlcov",
}

// IgnoreSet decides whether a path (relative to the workDir) should be
// skipped by the checkpoint machinery. Reading the path through read_file /
// view is still allowed — only checkpointing is suppressed.
type IgnoreSet struct {
	dirNames  []string // matched against each path component
	fileGlobs []string // matched (filepath.Match) against the basename
	cache     map[string]bool
}

// NewIgnoreSet builds the merged set: built-in defaults first, then any
// entries from <workDir>/.agentignore. The project's .agentignore can add
// more patterns but cannot un-ignore defaults (negation not supported in v1).
//
// .agentignore syntax: one pattern per line, # for comments, ! for negation
// (ignored in v1). Directory patterns should NOT have a trailing slash
// (matches the defaults). Globs (* ? [) match the basename.
func NewIgnoreSet(workDir string) (*IgnoreSet, error) {
	ig := &IgnoreSet{
		dirNames:  append([]string{}, defaultIgnorePatterns...),
		fileGlobs: nil,
		cache:     make(map[string]bool),
	}

	f, err := os.Open(filepath.Join(workDir, ".agentignore"))
	if err != nil {
		if os.IsNotExist(err) {
			return ig, nil
		}
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip leading "!" (negation) for v1; treat as a normal pattern.
		line = strings.TrimPrefix(line, "!")
		// Strip trailing "/" (gitignore directory marker).
		line = strings.TrimSuffix(line, "/")
		if line == "" {
			continue
		}
		if strings.ContainsAny(line, "*?[") {
			ig.fileGlobs = append(ig.fileGlobs, line)
		} else {
			ig.dirNames = append(ig.dirNames, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return ig, nil
}

// IsIgnored returns true if relPath (forward-slash, relative to workDir) is
// covered by any pattern. Walks path components for dir names; matches the
// basename against globs.
func (ig *IgnoreSet) IsIgnored(relPath string) bool {
	if relPath == "" {
		return false
	}
	relPath = filepath.ToSlash(strings.TrimPrefix(relPath, "./"))
	if relPath == "" {
		return false
	}
	if cached, ok := ig.cache[relPath]; ok {
		return cached
	}

	parts := strings.Split(relPath, "/")
	for _, part := range parts {
		for _, name := range ig.dirNames {
			if part == name {
				ig.cache[relPath] = true
				return true
			}
		}
	}

	base := parts[len(parts)-1]
	for _, pattern := range ig.fileGlobs {
		if matched, _ := filepath.Match(pattern, base); matched {
			ig.cache[relPath] = true
			return true
		}
	}

	ig.cache[relPath] = false
	return false
}
