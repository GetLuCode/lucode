package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/plugins"
)

// A bundled plugin belongs to the binary: the version in its manifest may not change while its
// files do, so what counts is the sha256. At every start an installed one whose copy in the binary
// differs is upgraded through the normal pipeline. It can run from an instance that dies right after
// (one that loses the port to a hub still running), so it keeps what that hub is reading from, does
// nothing the second time, and never stops the start.

// oldVersionsKeptFor is how long a replaced version stays on disk for a hub still running on it.
const oldVersionsKeptFor = 24 * time.Hour

const bundleCheckExport = "lucode-bundle-check"

func (h *Host) syncBundled() {
	entries, err := fs.ReadDir(h.deps.Bundled, ".")
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(h.deps.Bundled, path.Join(e.Name(), "plugin.json"))
		if err != nil {
			continue
		}
		// only what it takes to find its row: validating comes later, and failing there must show
		var head struct {
			ID      string `json:"id"`
			Version string `json:"version"`
		}
		if json.Unmarshal(raw, &head) != nil {
			log.Printf("[plugins] bundled %s: plugin.json cannot be read", e.Name())
			continue
		}
		l, err := h.get(head.ID)
		if err != nil || l.row.Source != sourceBundled {
			continue // not installed, or removed: it stays that way
		}
		if err := h.syncOne(l, e.Name(), head.Version); err != nil {
			h.bundleFailed(l, err)
		}
	}
	h.pruneOldVersions()
}

func (h *Host) syncOne(l *loadedPlugin, dir, version string) error {
	tmp, err := h.zipBundled(dir)
	if err != nil {
		return err
	}
	defer os.Remove(tmp)
	sha, err := plugins.FileSHA256(tmp)
	if err != nil {
		return err
	}
	if sha == l.row.SHA256 && l.pkg != nil {
		return nil
	}
	// an older binary leaves a newer version alone: its data may have moved on. Nothing is lost
	// either way, because an upgrade never touches settings, storage or conversations.
	if l.pkg != nil && olderVersion(version, l.row.Version) {
		msg := fmt.Sprintf("this lucode ships version %s, older than the installed %s: the installed one stays", version, l.row.Version)
		log.Printf("[plugins] %s: %s", l.row.ID, msg)
		if l.rt != nil {
			l.rt.Log("warn", msg)
		}
		return nil
	}
	res, err := h.stage(tmp, true)
	if err != nil {
		return err
	}
	h.mu.Lock()
	s := h.staged[res.Token]
	delete(h.staged, res.Token)
	h.mu.Unlock()
	defer os.RemoveAll(s.dir)
	if err := h.checkCandidate(l, s); err != nil {
		return err
	}
	// the grant follows the manifest: it ships inside the binary the user chose to run
	granted := make([]string, 0, len(res.Permissions))
	added := []string{}
	for _, p := range res.Permissions {
		granted = append(granted, p.Key)
		if !contains(l.row.Granted, p.Key) {
			added = append(added, p.Key)
		}
	}
	s.keepOld = true
	from := l.row.SHA256
	if _, err := h.commit(s, granted, sourceBundled); err != nil {
		return err
	}
	msg := fmt.Sprintf("updated from the copy in this lucode (%.8s to %.8s)", from, s.sha)
	if len(added) > 0 {
		msg += "; permissions added: " + strings.Join(added, ", ")
	}
	log.Printf("[plugins] %s: %s", l.row.ID, msg)
	if nl, err := h.get(l.row.ID); err == nil && nl.rt != nil {
		nl.rt.Log("info", msg)
	}
	return nil
}

// checkCandidate does what could go wrong after the switch, before it: the code must load and the
// blueprints must validate, against the new copy's own nodes. This runs at startup, before anything
// else reads the list it borrows for a moment.
func (h *Host) checkCandidate(l *loadedPlugin, s *stagedPackage) error {
	pkg := &plugins.Package{Dir: filepath.Join(s.dir, "unpacked"), Manifest: s.manifest, SHA256: s.sha}
	for _, target := range []string{plugins.TargetServer, plugins.TargetRunner} {
		if s.manifest.Target(target) == nil {
			continue
		}
		rt := plugins.NewRuntime(plugins.Options{Package: pkg, Target: target, HTTP: h.deps.HTTP})
		_, err := rt.Invoke(context.Background(), plugins.Invocation{Path: []string{bundleCheckExport}, Timeout: 10 * time.Second})
		rt.Close()
		if err != nil && !errors.Is(err, plugins.ErrNoHandler) {
			return fmt.Errorf("its %s code: %w", target, err)
		}
	}
	if h.deps.Services == nil {
		return nil
	}
	h.mu.Lock()
	h.loaded[l.row.ID] = &loadedPlugin{row: l.row, manifest: s.manifest}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		h.loaded[l.row.ID] = l
		h.mu.Unlock()
	}()
	for _, b := range s.manifest.Contributes.Blueprints {
		raw, err := pkg.ReadFile(b.File, plugins.MaxFileBytes)
		if err != nil {
			return fmt.Errorf("blueprint %s: %w", b.Key, err)
		}
		if bad := h.deps.Services.ValidateGraph(raw); len(bad) > 0 {
			return fmt.Errorf("blueprint %s: %s", b.Key, strings.Join(bad, "; "))
		}
	}
	return nil
}

// bundleFailed keeps the working version and says so. It is tried again at the next start.
func (h *Host) bundleFailed(l *loadedPlugin, err error) {
	msg := "the copy of this plugin in this lucode could not be installed, so the previous version stays: " + plugins.Scrub(err.Error())
	log.Printf("[plugins] %s: %s", l.row.ID, msg)
	l.bundleError = msg
	l.row.Error = msg
	if err := dbpkg.SetPluginError(h.deps.DB, l.row.ID, msg); err != nil {
		log.Printf("[plugins] %s: %v", l.row.ID, err)
	}
	if l.rt != nil {
		l.rt.Log("error", msg)
	}
}

// pruneOldVersions removes versions replaced long enough ago that no running hub reads them.
func (h *Host) pruneOldVersions() {
	h.mu.RLock()
	current := map[string]bool{}
	ids := []string{}
	for id, l := range h.loaded {
		ids = append(ids, id)
		current[l.row.Dir], current[l.row.Zip] = true, true
	}
	h.mu.RUnlock()
	for _, id := range ids {
		entries, err := os.ReadDir(filepath.Join(h.root, id))
		if err != nil {
			continue
		}
		for _, e := range entries {
			file := filepath.Join(h.root, id, e.Name())
			info, err := e.Info()
			if err != nil || current[file] || time.Since(info.ModTime()) < oldVersionsKeptFor {
				continue
			}
			_ = os.RemoveAll(file)
		}
	}
}

// olderVersion compares the numbers of two semver versions.
func olderVersion(a, b string) bool {
	pa, pb := versionParts(a), versionParts(b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func versionParts(v string) [3]int {
	var out [3]int
	v, _, _ = strings.Cut(v, "-")
	v, _, _ = strings.Cut(v, "+")
	for i, part := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(part)
	}
	return out
}
