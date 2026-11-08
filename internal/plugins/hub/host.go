// Package hub is the plugin host of the lucode hub: what is installed, the server target's SDK,
// and the signed road to the runners. cmd/lucode names it and nothing else links it, so a runner
// binary carries no part of it.
package hub

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/plugins"
	"claude-orchestrator/internal/plugins/bundled"
)

const stageTTL = 15 * time.Minute

// sourceBundled marks a plugin that came from the binary.
const sourceBundled = "bundled"

// Host implements plugins.HubHost.
type Host struct {
	deps   plugins.HubDeps
	signer *plugins.Signer
	root   string // <data>/plugins
	ctx    context.Context

	ops sync.Mutex // one install, upgrade or removal at a time

	mu     sync.RWMutex
	loaded map[string]*loadedPlugin
	staged map[string]*stagedPackage

	remote *remote
}

// loadedPlugin is an installed plugin in memory.
type loadedPlugin struct {
	row      dbpkg.PluginRow
	manifest plugins.Manifest
	pkg      *plugins.Package
	rt       *plugins.Runtime // also keeps the log of a plugin that only has a runner target

	secrets atomic.Value // []string: values never to show

	allowMu sync.Mutex
	allowed map[string]bool // rpcs view trees of this plugin offered

	noEvent sync.Map // events the plugin turned out not to handle

	bundleError string // why the copy in the binary could not replace this one

	browserMu sync.Mutex
	browser   plugins.BrowserDriver
}

type stagedPackage struct {
	dir      string // the staging folder, holding package.zip and unpacked/
	manifest plugins.Manifest
	sha      string
	bundled  bool
	expires  time.Time
	keepOld  bool // a hub still running may be reading the version this replaces
}

// New builds the host. The zip road to runners starts with Start.
func New(deps plugins.HubDeps) (plugins.HubHost, error) {
	signer, err := plugins.LoadOrCreateSigner(deps.DataDir)
	if err != nil {
		return nil, err
	}
	if deps.Bundled == nil {
		deps.Bundled = bundled.FS
	}
	if deps.MaxDepth == 0 {
		deps.MaxDepth = 8
	}
	h := &Host{
		deps: deps, signer: signer, root: filepath.Join(deps.DataDir, "plugins"), ctx: context.Background(),
		loaded: map[string]*loadedPlugin{}, staged: map[string]*stagedPackage{},
	}
	h.remote = newRemote(h)
	if deps.Relay != nil {
		deps.Relay.SetHandler(h.deliverWebhook)
	}
	return h, nil
}

// Start loads what is installed. A plugin that fails to load stays listed with its error.
func (h *Host) Start(ctx context.Context) error {
	h.ctx = ctx
	_ = os.RemoveAll(filepath.Join(h.root, ".staging")) // packages somebody never finished installing
	rows, err := dbpkg.ListPlugins(h.deps.DB)
	if err != nil {
		return err
	}
	for _, row := range rows {
		l, err := h.load(row)
		if err != nil {
			log.Printf("[plugins] %s: %v", row.ID, err)
			_ = dbpkg.SetPluginError(h.deps.DB, row.ID, err.Error())
			row.Error = err.Error()
		}
		h.mu.Lock()
		h.loaded[row.ID] = l
		h.mu.Unlock()
		if err == nil {
			h.seedBlueprints(l)
		}
	}
	h.syncBundled()
	for _, id := range h.deps.AutoInstall {
		if id == plugins.GeminiID {
			// the history comes first, and only once: removed with the plugin, it does not come back
			if n, err := dbpkg.CopyAIModeConversationsOnce(h.deps.DB, id, geminiSource); err != nil {
				log.Printf("[plugins] copy the AI Mode conversations: %v", err)
			} else if n > 0 {
				log.Printf("[plugins] copied %d AI Mode conversations", n)
			}
		}
		h.autoInstall(id)
	}
	return h.remote.start(ctx)
}

// autoInstall installs a bundled plugin the first time this hub sees it. Removed later, it stays
// removed, and the catalog still offers it. If the install fails it is tried on the next start.
func (h *Host) autoInstall(id string) {
	first, err := dbpkg.ClaimPluginSeed(h.deps.DB, id)
	if err != nil || !first {
		return
	}
	if _, err := h.get(id); err == nil {
		return // installed by hand already
	}
	if _, err := h.InstallBundled(id); err != nil {
		log.Printf("[plugins] install %s: %v", id, err)
		if err := dbpkg.ReleasePluginSeed(h.deps.DB, id); err != nil {
			log.Printf("[plugins] release %s: %v", id, err)
		}
	}
}

// load builds a plugin's memory from its row. On failure the plugin stays listed, with no code.
func (h *Host) load(row dbpkg.PluginRow) (*loadedPlugin, error) {
	l := &loadedPlugin{row: row, allowed: map[string]bool{}}
	l.secrets.Store([]string{})
	if err := json.Unmarshal(row.Manifest, &l.manifest); err != nil {
		return l, errors.New("its stored manifest cannot be read")
	}
	if _, err := os.Stat(filepath.Join(row.Dir, "plugin.json")); err != nil {
		return l, errors.New("its files are gone: install it again")
	}
	l.pkg = &plugins.Package{Dir: row.Dir, Manifest: l.manifest, SHA256: row.SHA256}
	l.rt = plugins.NewRuntime(plugins.Options{
		Package: l.pkg, Target: plugins.TargetServer, HTTP: h.deps.HTTP, ScrubLog: l.scrubText,
	})
	h.refreshSecrets(l)
	return l, nil
}

func (h *Host) refreshSecrets(l *loadedPlugin) {
	stored, err := dbpkg.PluginSettings(h.deps.DB, l.row.ID)
	if err != nil {
		return
	}
	list := []string{}
	for _, def := range l.manifest.Contributes.Settings {
		if def.Type != "secret" {
			continue
		}
		var v string
		if json.Unmarshal([]byte(stored[def.Key]), &v) == nil && len(v) >= 4 {
			list = append(list, v)
		}
	}
	l.secrets.Store(list)
}

// scrubText takes the plugin's secret values out of anything that leaves the hub.
func (l *loadedPlugin) scrubText(s string) string {
	list, _ := l.secrets.Load().([]string)
	for _, v := range list {
		s = strings.ReplaceAll(s, v, "***")
	}
	return s
}

func (l *loadedPlugin) scrubValue(v any) any {
	switch x := v.(type) {
	case string:
		return l.scrubText(x)
	case []any:
		for i := range x {
			x[i] = l.scrubValue(x[i])
		}
	case map[string]any:
		for k := range x {
			x[k] = l.scrubValue(x[k])
		}
	}
	return v
}

func (h *Host) get(id string) (*loadedPlugin, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	l, ok := h.loaded[id]
	if !ok {
		return nil, plugins.ErrNotFound
	}
	return l, nil
}

// usable returns a plugin that can run code now.
func (h *Host) usable(id string) (*loadedPlugin, error) {
	l, err := h.get(id)
	if err != nil {
		return nil, fmt.Errorf("the plugin %s is not installed", id)
	}
	if !l.row.Enabled {
		return nil, fmt.Errorf("the plugin %s is disabled", l.manifest.Name)
	}
	if l.rt == nil {
		return nil, fmt.Errorf("the plugin %s could not be loaded: %s", l.manifest.Name, l.row.Error)
	}
	return l, nil
}

func view(l *loadedPlugin) plugins.PluginView {
	v := plugins.PluginView{
		ID: l.row.ID, Name: l.manifest.Name, Version: l.row.Version, Manifest: l.manifest,
		Enabled: l.row.Enabled, Granted: l.row.Granted, Error: l.row.Error, Source: l.row.Source,
		Targets: map[string]any{},
	}
	if v.Granted == nil {
		v.Granted = []string{}
	}
	switch {
	case l.row.Error != "":
		v.Status = "error"
	case l.row.Enabled:
		v.Status = "enabled"
	default:
		v.Status = "disabled"
	}
	if t := l.manifest.Targets.Server; t != nil {
		v.Targets[plugins.TargetServer] = t
	}
	if t := l.manifest.Targets.Runner; t != nil {
		v.Targets[plugins.TargetRunner] = t
	}
	return v
}

// List shows what is installed.
func (h *Host) List() []plugins.PluginView {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := []plugins.PluginView{}
	for _, id := range plugins.SortedKeys(h.loaded) {
		out = append(out, view(h.loaded[id]))
	}
	return out
}

// Get shows one plugin.
func (h *Host) Get(id string) (plugins.PluginView, error) {
	l, err := h.get(id)
	if err != nil {
		return plugins.PluginView{}, err
	}
	return view(l), nil
}

// ---- install ----

func newToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (h *Host) cleanStaged() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for token, s := range h.staged {
		if time.Now().After(s.expires) {
			os.RemoveAll(s.dir)
			delete(h.staged, token)
		}
	}
}

// Inspect validates an upload and stages it for the admin's review. Nothing is installed.
func (h *Host) Inspect(zipFile string) (plugins.InspectResult, error) {
	return h.stage(zipFile, false)
}

func (h *Host) stage(zipFile string, isBundled bool) (plugins.InspectResult, error) {
	h.cleanStaged()
	m, err := plugins.CheckZip(zipFile, plugins.ParseOptions{Bundled: isBundled})
	if err != nil {
		return plugins.InspectResult{}, err
	}
	token := newToken()
	dir := filepath.Join(h.root, ".staging", token)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return plugins.InspectResult{}, err
	}
	fail := func(err error) (plugins.InspectResult, error) {
		os.RemoveAll(dir)
		return plugins.InspectResult{}, err
	}
	if err := plugins.ExtractZip(zipFile, filepath.Join(dir, "unpacked")); err != nil {
		return fail(err)
	}
	if err := copyFile(zipFile, filepath.Join(dir, "package.zip")); err != nil {
		return fail(err)
	}
	sha, err := plugins.FileSHA256(filepath.Join(dir, "package.zip"))
	if err != nil {
		return fail(err)
	}
	res := plugins.InspectResult{
		Token: token, Manifest: m, Permissions: plugins.PermissionsOf(m),
		NewPermissions: []string{}, Warnings: h.warnings(m),
	}
	for _, p := range res.Permissions {
		res.NewPermissions = append(res.NewPermissions, p.Key)
	}
	if old, err := h.get(m.ID); err == nil {
		res.UpgradeFrom = old.row.Version
		res.NewPermissions = res.NewPermissions[:0]
		for _, p := range res.Permissions {
			if !contains(old.row.Granted, p.Key) {
				res.NewPermissions = append(res.NewPermissions, p.Key)
			}
		}
	}
	h.mu.Lock()
	h.staged[token] = &stagedPackage{dir: dir, manifest: m, sha: sha, bundled: isBundled, expires: time.Now().Add(stageTTL)}
	h.mu.Unlock()
	return res, nil
}

func (h *Host) warnings(m plugins.Manifest) []string {
	out := []string{}
	if r := m.Targets.Runner; r != nil {
		if r.Permissions.Exec != nil {
			out = append(out, "Running commands only works on runners started with --plugin-policy=full.")
		}
		out = append(out, "Its runner code is delivered to every online runner whose policy allows plugins.")
	}
	for _, p := range m.Contributes.Pages {
		if p.UI == "" {
			continue
		}
		calls := "no calls"
		if len(p.RPC) > 0 {
			calls = "only these calls: " + strings.Join(p.RPC, ", ")
		}
		out = append(out, fmt.Sprintf("Its page %q draws itself: its code runs in your browser, apart from lucode, with no network, and reaches its plugin through %s.", p.Label, calls))
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// Install takes a staged package once. The grant must cover everything it asks for.
func (h *Host) Install(token string, granted []string) (plugins.PluginView, error) {
	h.mu.Lock()
	s, ok := h.staged[token]
	delete(h.staged, token) // single use, whatever happens next
	h.mu.Unlock()
	if !ok || time.Now().After(s.expires) {
		if ok {
			os.RemoveAll(s.dir)
		}
		return plugins.PluginView{}, errors.New("this upload has expired or was already used: upload the zip again")
	}
	defer os.RemoveAll(s.dir)
	for _, p := range plugins.PermissionsOf(s.manifest) {
		if !contains(granted, p.Key) {
			return plugins.PluginView{}, fmt.Errorf("the permission %q was not granted: a plugin is installed with everything it asks for, or not at all", p.Key)
		}
	}
	source := "upload"
	if s.bundled {
		source = sourceBundled
	}
	return h.commit(s, granted, source)
}

// commit moves a staged package into place and makes it the running version.
func (h *Host) commit(s *stagedPackage, granted []string, source string) (plugins.PluginView, error) {
	h.ops.Lock()
	defer h.ops.Unlock()
	m := s.manifest
	// only what this manifest asks for is kept: a grant cannot carry over to a later upgrade
	keep := []string{}
	for _, p := range plugins.PermissionsOf(m) {
		if contains(granted, p.Key) {
			keep = append(keep, p.Key)
		}
	}
	base := filepath.Join(h.root, m.ID)
	if err := os.MkdirAll(base, 0o755); err != nil {
		return plugins.PluginView{}, err
	}
	tag := s.sha[:16]
	dest, zipDest := filepath.Join(base, tag), filepath.Join(base, tag+".zip")
	if _, err := os.Stat(dest); err == nil {
		// the same bytes again: the old folder is what is running, and it gets replaced below
		os.RemoveAll(dest)
		os.Remove(zipDest)
	}
	if err := os.Rename(filepath.Join(s.dir, "unpacked"), dest); err != nil {
		return plugins.PluginView{}, err
	}
	if err := os.Rename(filepath.Join(s.dir, "package.zip"), zipDest); err != nil {
		os.RemoveAll(dest)
		return plugins.PluginView{}, err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return plugins.PluginView{}, err
	}
	old, _ := h.get(m.ID)
	enabled := old == nil || old.row.Enabled
	row := dbpkg.PluginRow{
		ID: m.ID, Version: m.Version, Manifest: raw, Dir: dest, Zip: zipDest, SHA256: s.sha,
		Enabled: enabled, Granted: keep, Source: source,
	}
	if err := dbpkg.SavePlugin(h.deps.DB, row); err != nil {
		return plugins.PluginView{}, err
	}
	l, err := h.load(row)
	if err != nil {
		return plugins.PluginView{}, err
	}
	h.mu.Lock()
	h.loaded[m.ID] = l
	h.mu.Unlock()
	if old != nil {
		if old.rt != nil {
			old.rt.Close()
		}
		h.closeBrowser(old)
		if old.row.Dir != dest && old.row.Dir != "" {
			if s.keepOld {
				// pruneOldVersions counts from now
				now := time.Now()
				_ = os.Chtimes(old.row.Dir, now, now)
				_ = os.Chtimes(old.row.Zip, now, now)
			} else {
				os.RemoveAll(old.row.Dir)
				os.Remove(old.row.Zip)
			}
		}
	}
	h.seedBlueprints(l)
	h.changed()
	h.remote.kick()
	name := "installed"
	if old != nil {
		name = "upgraded"
	}
	h.fireEvent(l, name, "", map[string]any{"version": m.Version}, 0)
	return view(l), nil
}

func (h *Host) changed() {
	if h.deps.Services != nil {
		h.deps.Services.Broadcast("", map[string]any{"type": "plugins_changed"})
	}
}

// SetEnabled switches a plugin on or off. Off closes its browser, stops what it has running
// and takes it off the runners.
func (h *Host) SetEnabled(id string, enabled bool) (plugins.PluginView, error) {
	h.ops.Lock()
	defer h.ops.Unlock()
	l, err := h.get(id)
	if err != nil {
		return plugins.PluginView{}, err
	}
	if l.row.Enabled == enabled {
		return view(l), nil
	}
	if err := dbpkg.SetPluginEnabled(h.deps.DB, id, enabled); err != nil {
		return plugins.PluginView{}, err
	}
	row := l.row
	row.Enabled = enabled
	if !enabled {
		h.closeBrowser(l)
		if l.rt != nil {
			l.rt.Close()
		}
		h.remote.unload(id)
	}
	nl, err := h.load(row)
	if err != nil {
		nl.row.Error = err.Error()
	}
	nl.bundleError = l.bundleError
	h.mu.Lock()
	h.loaded[id] = nl
	h.mu.Unlock()
	h.changed()
	if enabled {
		h.remote.kick()
	}
	return view(nl), nil
}

// Uninstall removes a plugin and its locked flows; copies of them stay, and show what is missing.
func (h *Host) Uninstall(id string, keepData bool) error {
	l, err := h.get(id)
	if err != nil {
		return err
	}
	if l.rt != nil && l.row.Enabled {
		h.fireEventWait(l, "uninstalling", "", map[string]any{}, 0)
	}
	h.ops.Lock()
	defer h.ops.Unlock()
	h.closeBrowser(l)
	if l.rt != nil {
		l.rt.Close()
	}
	h.remote.unload(id)
	flows, err := dbpkg.LockedFlowsOf(h.deps.DB, id)
	if err != nil {
		return err
	}
	for _, f := range flows {
		if err := dbpkg.DeleteFlow(h.deps.DB, f.ID); err != nil {
			return err
		}
	}
	var endpoints []dbpkg.RelayEndpointRow
	if !keepData {
		endpoints, _ = dbpkg.ListRelayEndpoints(h.deps.DB, id)
	}
	if err := dbpkg.DeletePlugin(h.deps.DB, id, keepData); err != nil {
		return err
	}
	h.forgetEndpoints(endpoints)
	h.mu.Lock()
	delete(h.loaded, id)
	h.mu.Unlock()
	os.RemoveAll(filepath.Join(h.root, id))
	if !keepData {
		os.RemoveAll(filepath.Join(h.deps.DataDir, "plugin-data", id))
	}
	h.changed()
	return nil
}

// ---- blueprints ----

// SeedProject gives a project the blueprints of what is installed.
func (h *Host) SeedProject(projectID string) {
	h.mu.RLock()
	list := make([]*loadedPlugin, 0, len(h.loaded))
	for _, l := range h.loaded {
		list = append(list, l)
	}
	h.mu.RUnlock()
	for _, l := range list {
		h.seedBlueprintsIn(l, []string{projectID})
	}
}

func (h *Host) seedBlueprints(l *loadedPlugin) {
	projects, err := dbpkg.ListProjects(h.deps.DB)
	if err != nil {
		log.Printf("[plugins] list projects: %v", err)
		return
	}
	ids := make([]string, 0, len(projects))
	for _, p := range projects {
		ids = append(ids, p.ID)
	}
	h.seedBlueprintsIn(l, ids)
	// blueprints a new version no longer has
	flows, err := dbpkg.LockedFlowsOf(h.deps.DB, l.row.ID)
	if err != nil {
		return
	}
	keep := map[string]bool{}
	for _, b := range l.manifest.Contributes.Blueprints {
		keep[b.Key] = true
	}
	for _, f := range flows {
		if !keep[f.PluginBlueprint] {
			_ = dbpkg.DeleteFlow(h.deps.DB, f.ID)
		}
	}
}

func (h *Host) seedBlueprintsIn(l *loadedPlugin, projects []string) {
	if l.pkg == nil {
		return
	}
	var problems []string
	for _, b := range l.manifest.Contributes.Blueprints {
		raw, err := l.pkg.ReadFile(b.File, plugins.MaxFileBytes)
		if err != nil {
			problems = append(problems, b.Key+": "+err.Error())
			continue
		}
		if h.deps.Services != nil {
			if bad := h.deps.Services.ValidateGraph(raw); len(bad) > 0 {
				problems = append(problems, b.Key+": "+strings.Join(bad, "; "))
				continue
			}
		}
		for _, pid := range projects {
			if _, err := dbpkg.UpsertLockedFlow(h.deps.DB, pid, l.row.ID, b.Key, b.Name, raw); err != nil {
				problems = append(problems, b.Key+": "+err.Error())
			}
		}
	}
	msg := l.bundleError
	if len(problems) > 0 {
		text := "blueprints not added: " + strings.Join(problems, " | ")
		if l.rt != nil {
			l.rt.Log("error", text)
		}
		if msg != "" {
			msg += " | "
		}
		msg += text
	}
	if msg != l.row.Error {
		l.row.Error = msg
		_ = dbpkg.SetPluginError(h.deps.DB, l.row.ID, msg)
	}
}

// ---- catalog ----

// bundledDirs finds the plugins in the binary.
func (h *Host) bundledDirs() map[string]plugins.Manifest {
	out := map[string]plugins.Manifest{}
	entries, err := fs.ReadDir(h.deps.Bundled, ".")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		raw, err := fs.ReadFile(h.deps.Bundled, path.Join(e.Name(), "plugin.json"))
		if err != nil {
			continue
		}
		m, err := plugins.ParseManifest(raw, plugins.ParseOptions{Bundled: true})
		if err != nil {
			log.Printf("[plugins] bundled %s: %v", e.Name(), err)
			continue
		}
		out[e.Name()] = m
	}
	return out
}

// Catalog lists the plugins that ship with lucode.
func (h *Host) Catalog() []plugins.CatalogEntry {
	out := []plugins.CatalogEntry{}
	for _, m := range h.bundledDirs() {
		_, err := h.get(m.ID)
		out = append(out, plugins.CatalogEntry{ID: m.ID, Name: m.Name, Description: m.Description, Installed: err == nil})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// InstallBundled installs a plugin of the binary, through the same validation as an upload.
func (h *Host) InstallBundled(id string) (plugins.PluginView, error) {
	for dir, m := range h.bundledDirs() {
		if m.ID != id {
			continue
		}
		tmp, err := h.zipBundled(dir)
		if err != nil {
			return plugins.PluginView{}, err
		}
		defer os.Remove(tmp)
		res, err := h.stage(tmp, true)
		if err != nil {
			return plugins.PluginView{}, err
		}
		granted := make([]string, 0, len(res.Permissions))
		for _, p := range res.Permissions {
			granted = append(granted, p.Key)
		}
		return h.Install(res.Token, granted)
	}
	return plugins.PluginView{}, plugins.ErrNotFound
}

func (h *Host) zipBundled(dir string) (string, error) {
	if err := os.MkdirAll(h.root, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(h.root, "bundled-*.zip")
	if err != nil {
		return "", err
	}
	defer f.Close()
	w := zip.NewWriter(f)
	err = fs.WalkDir(h.deps.Bundled, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(h.deps.Bundled, p)
		if err != nil {
			return err
		}
		out, err := w.Create(strings.TrimPrefix(p, dir+"/"))
		if err != nil {
			return err
		}
		_, err = io.Copy(out, bytes.NewReader(raw))
		return err
	})
	if err == nil {
		err = w.Close()
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// ---- settings ----

// Settings are the values as the browser sees them: a secret only says whether it is set.
func (h *Host) Settings(id string) (map[string]any, error) {
	l, err := h.get(id)
	if err != nil {
		return nil, err
	}
	stored, err := dbpkg.PluginSettings(h.deps.DB, id)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, def := range l.manifest.Contributes.Settings {
		raw, has := stored[def.Key]
		if def.Type == "secret" {
			out[def.Key] = map[string]any{"set": has}
			continue
		}
		if !has {
			out[def.Key] = def.Default
			continue
		}
		var v any
		if json.Unmarshal([]byte(raw), &v) == nil {
			out[def.Key] = v
		}
	}
	return out, nil
}

// PutSettings stores values the manifest declares. An empty value forgets it, and a secret sent
// back as {"set":true} stays as it is.
func (h *Host) PutSettings(id string, values map[string]any) error {
	l, err := h.get(id)
	if err != nil {
		return err
	}
	defs := map[string]plugins.SettingDef{}
	for _, d := range l.manifest.Contributes.Settings {
		defs[d.Key] = d
	}
	for key, v := range values {
		def, ok := defs[key]
		if !ok {
			return fmt.Errorf("this plugin has no setting %q", key)
		}
		if m, isMap := v.(map[string]any); isMap && def.Type == "secret" && len(m) == 1 && m["set"] != nil {
			continue
		}
		if v == nil || v == "" {
			if err := dbpkg.DeletePluginSetting(h.deps.DB, id, key); err != nil {
				return err
			}
			continue
		}
		switch def.Type {
		case "number":
			if _, ok := v.(float64); !ok {
				return fmt.Errorf("the setting %q must be a number", key)
			}
		case "bool":
			if _, ok := v.(bool); !ok {
				return fmt.Errorf("the setting %q must be on or off", key)
			}
		case "select":
			if s, ok := v.(string); !ok || !contains(def.Options, s) {
				return fmt.Errorf("the setting %q: not one of its options", key)
			}
		default:
			if _, ok := v.(string); !ok {
				return fmt.Errorf("the setting %q must be text", key)
			}
		}
		raw, _ := json.Marshal(v)
		if len(raw) > 1<<20 {
			return fmt.Errorf("the setting %q is too long", key)
		}
		if err := dbpkg.SetPluginSetting(h.deps.DB, id, key, string(raw)); err != nil {
			return err
		}
	}
	h.refreshSecrets(l)
	return nil
}

// Logs returns the plugin's log.
func (h *Host) Logs(id string) ([]plugins.LogEntry, error) {
	l, err := h.get(id)
	if err != nil {
		return nil, err
	}
	if l.rt == nil {
		return []plugins.LogEntry{}, nil
	}
	return l.rt.Logs(), nil
}

// Asset reads an image of the package. Nothing but images is served.
func (h *Host) Asset(id, rel string) ([]byte, string, error) {
	l, err := h.get(id)
	if err != nil {
		return nil, "", err
	}
	if l.pkg == nil {
		return nil, "", plugins.ErrNotFound
	}
	ctype := map[string]string{
		".png": "image/png", ".svg": "image/svg+xml", ".webp": "image/webp", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	}[strings.ToLower(path.Ext(rel))]
	if ctype == "" {
		return nil, "", errors.New("only images are served")
	}
	raw, err := l.pkg.ReadFile(rel, plugins.MaxFileBytes)
	if err != nil {
		return nil, "", plugins.ErrNotFound
	}
	return raw, ctype, nil
}

// PageUI is a frontend page's script and stylesheet (empty when it has none). The browser runs the
// script in a worker of its own; the plugin must be on.
func (h *Host) PageUI(id, pageID string) (plugins.PageDef, []byte, []byte, plugins.Manifest, error) {
	l, err := h.usable(id)
	if err != nil {
		return plugins.PageDef{}, nil, nil, plugins.Manifest{}, err
	}
	if l.pkg == nil {
		return plugins.PageDef{}, nil, nil, l.manifest, plugins.ErrNotFound
	}
	for _, p := range l.manifest.Contributes.Pages {
		if p.ID != pageID {
			continue
		}
		if p.UI == "" {
			return p, nil, nil, l.manifest, fmt.Errorf("the page %q of %s draws view trees and has no frontend", pageID, l.manifest.Name)
		}
		script, err := l.pkg.ReadFile(p.UI, plugins.MaxCodeBytes)
		if err != nil {
			return p, nil, nil, l.manifest, plugins.ErrNotFound
		}
		var style []byte
		if _, serr := os.Stat(filepath.Join(l.pkg.Dir, filepath.FromSlash(p.Style()))); serr == nil {
			if style, err = l.pkg.ReadFile(p.Style(), plugins.MaxStyleBytes); err != nil {
				return p, nil, nil, l.manifest, err
			}
		}
		return p, script, style, l.manifest, nil
	}
	return plugins.PageDef{}, nil, nil, l.manifest, plugins.ErrNotFound
}

// UI merges what the enabled plugins add to the web.
func (h *Host) UI() plugins.UIContributions {
	ui := plugins.UIContributions{
		Models: []map[string]any{}, Conversations: []map[string]any{}, Pages: []map[string]any{},
		ChatActions: []map[string]any{}, CardActions: []map[string]any{},
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, id := range plugins.SortedKeys(h.loaded) {
		l := h.loaded[id]
		if !l.row.Enabled || l.rt == nil {
			continue
		}
		c := l.manifest.Contributes
		ui.Models = appendEntries(ui.Models, l, c.Models)
		ui.Conversations = appendEntries(ui.Conversations, l, c.Conversations)
		ui.Pages = appendEntries(ui.Pages, l, c.Pages)
		ui.ChatActions = appendEntries(ui.ChatActions, l, c.ChatActions)
		ui.CardActions = appendEntries(ui.CardActions, l, c.CardActions)
	}
	return ui
}

func appendEntries[T any](out []map[string]any, l *loadedPlugin, list []T) []map[string]any {
	for _, item := range list {
		raw, _ := json.Marshal(item)
		entry := map[string]any{}
		_ = json.Unmarshal(raw, &entry)
		entry["pluginId"], entry["pluginName"] = l.row.ID, l.manifest.Name
		out = append(out, entry)
	}
	return out
}
