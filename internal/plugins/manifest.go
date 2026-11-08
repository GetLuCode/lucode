// Package plugins is the core both binaries use: the package format (zip and manifest), the
// script sandbox with its base SDK, and the signed messages between a hub and its runners. It
// imports neither the database nor the API. The hub's host is internal/plugins/hub and the
// runner's is internal/plugins/runner.
package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// Targets a plugin's code can run on.
const (
	TargetServer = "server"
	TargetRunner = "runner"
)

// MaxTimeoutSeconds bounds a model turn and a flow node.
const MaxTimeoutSeconds = 900

// DefaultNodeSeconds and DefaultTurnSeconds apply when the manifest gives no timeout.
const (
	DefaultNodeSeconds = 300
	DefaultTurnSeconds = 600
)

var (
	idRe       = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9-]+)+$`)
	semverRe   = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	localIDRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	rpcNameRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	portRe     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	providerRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	hostRe     = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)
	accentRe   = regexp.MustCompile(`^#[0-9a-fA-F]{3,8}$`)
	commandRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	claudeIDRe = regexp.MustCompile(`^(claude|sonnet|opus|haiku|fable|anthropic|ollama|local)([-_.:].*)?$`)
)

// Manifest is plugin.json after normalising: Targets always says where the code runs.
type Manifest struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Version     string  `json:"version"`
	SDK         int     `json:"sdk"`
	Description string  `json:"description,omitempty"`
	Author      string  `json:"author,omitempty"`
	Homepage    string  `json:"homepage,omitempty"`
	Icon        string  `json:"icon,omitempty"`
	Targets     Targets `json:"targets"`

	// Old form: main and permissions at the top mean the server target.
	Main        string       `json:"main,omitempty"`
	Permissions *Permissions `json:"permissions,omitempty"`

	Contributes Contributes `json:"contributes"`
	// Build is for lucode-plugin; the hub ignores it.
	Build *BuildDef `json:"build,omitempty"`
}

// Targets says which targets a plugin has.
type Targets struct {
	Server *Target `json:"server,omitempty"`
	Runner *Target `json:"runner,omitempty"`
}

// Target is the code of one target and what it may do.
type Target struct {
	Main        string      `json:"main"`
	Permissions Permissions `json:"permissions"`
}

// Permissions is what a target asks for. Each field belongs to one target.
type Permissions struct {
	// server
	Storage       bool         `json:"storage,omitempty"`
	Conversations bool         `json:"conversations,omitempty"`
	Sessions      string       `json:"sessions,omitempty"`
	Board         string       `json:"board,omitempty"`
	Flows         bool         `json:"flows,omitempty"`
	Browser       *BrowserPerm `json:"browser,omitempty"`
	Relay         *RelayPerm   `json:"relay,omitempty"`
	// runner
	FS   string    `json:"fs,omitempty"`
	Git  string    `json:"git,omitempty"`
	Exec *ExecPerm `json:"exec,omitempty"`
	// both
	HTTP *HTTPPerm `json:"http,omitempty"`
}

// HTTPPerm lists the hosts lucode.http may reach.
type HTTPPerm struct {
	Hosts []string `json:"hosts"`
}

// RelayPerm lists the kinds of sender (telegram, ...) whose webhooks the plugin may get through the
// hub's webhook relay. Each has its own grant, so an upgrade that adds one needs a new one.
type RelayPerm struct {
	Providers []string `json:"providers"`
}

// BrowserPerm lists the origins lucode.browser may visit.
type BrowserPerm struct {
	Origins []string `json:"origins"`
}

// ExecPerm lists the commands lucode.exec may start.
type ExecPerm struct {
	Commands []string `json:"commands"`
}

// Contributes is what a plugin adds to the app.
type Contributes struct {
	Models        []ModelDef        `json:"models,omitempty"`
	Conversations []ConversationDef `json:"conversations,omitempty"`
	Pages         []PageDef         `json:"pages,omitempty"`
	Settings      []SettingDef      `json:"settings,omitempty"`
	Blueprints    []BlueprintDef    `json:"blueprints,omitempty"`
	FlowNodes     []FlowNodeDef     `json:"flowNodes,omitempty"`
	ChatActions   []ActionDef       `json:"chatActions,omitempty"`
	CardActions   []ActionDef       `json:"cardActions,omitempty"`
}

// ModelDef is a model the chat can talk to.
type ModelDef struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	Mode           string `json:"mode,omitempty"`
	Hint           string `json:"hint,omitempty"`
	Icon           string `json:"icon,omitempty"`
	Accent         string `json:"accent,omitempty"`
	Images         bool   `json:"images,omitempty"`
	Loader         string `json:"loader,omitempty"`
	WaitingText    string `json:"waitingText,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
}

// ConversationDef is a history list of a model.
type ConversationDef struct {
	ID        string `json:"id"`
	Model     string `json:"model"`
	Title     string `json:"title"`
	EmptyText string `json:"emptyText,omitempty"`
	ShowURL   bool   `json:"showUrl,omitempty"`
}

// PageDef is a page of the plugin: either view trees an rpc returns (Render), or a frontend of its
// own (UI), a script the browser runs in a worker, whose only way out is the rpcs listed in RPC.
type PageDef struct {
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Icon   string   `json:"icon,omitempty"`
	Render string   `json:"render,omitempty"`
	UI     string   `json:"ui,omitempty"`
	RPC    []string `json:"rpc,omitempty"`
	// Placement says where the page lives: "" is a tab of the project, "settings" a pane of the
	// plugin's own page in Settings, labelled with the page's label.
	Placement string `json:"placement,omitempty"`
}

// Style is the stylesheet beside a frontend page's script: dist/main.js → dist/main.css.
func (d PageDef) Style() string {
	if d.UI == "" {
		return ""
	}
	return strings.TrimSuffix(d.UI, ".js") + ".css"
}

// MaxPageRPCs is how many rpcs one frontend page may call.
const MaxPageRPCs = 64

// BuildDef is what lucode-plugin build compiles, source to output. The hub never reads it: a zip
// holds the outputs, which the manifest names as usual (targets.*.main, pages[].ui).
type BuildDef struct {
	Server string            `json:"server,omitempty"`
	Runner string            `json:"runner,omitempty"`
	Pages  map[string]string `json:"pages,omitempty"`
}

// SettingDef is a value an admin sets.
type SettingDef struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Type    string   `json:"type"`
	Default any      `json:"default,omitempty"`
	Options []string `json:"options,omitempty"`
	Help    string   `json:"help,omitempty"`
}

// BlueprintDef is a flow graph that ships with the plugin.
type BlueprintDef struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	File        string `json:"file"`
}

// NodeField has the shape of a field of a base flow node.
type NodeField struct {
	Name      string   `json:"name"`
	Label     string   `json:"label"`
	Kind      string   `json:"kind"`
	Required  bool     `json:"required,omitempty"`
	Options   []string `json:"options,omitempty"`
	Default   any      `json:"default,omitempty"`
	Help      string   `json:"help,omitempty"`
	Min       *float64 `json:"min,omitempty"`
	PortLabel string   `json:"port_label,omitempty"`
}

// FlowNodeDef is a node the plugin adds to flows.
type FlowNodeDef struct {
	ID             string      `json:"id"`
	Label          string      `json:"label"`
	Description    string      `json:"description,omitempty"`
	Icon           string      `json:"icon,omitempty"`
	Fields         []NodeField `json:"fields,omitempty"`
	Ports          []string    `json:"ports,omitempty"`
	TimeoutSeconds int         `json:"timeoutSeconds,omitempty"`
	Target         string      `json:"target,omitempty"`
}

// ActionDef is a button that calls an rpc.
type ActionDef struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Icon   string `json:"icon,omitempty"`
	RPC    string `json:"rpc"`
	Target string `json:"target,omitempty"`
}

var nodeFieldKinds = map[string]bool{
	"text": true, "template": true, "expr": true, "number": true, "select": true,
	"agent": true, "list": true, "bool": true, "lines": true,
}

var settingTypes = map[string]bool{"text": true, "textarea": true, "number": true, "bool": true, "select": true, "secret": true}

// ParseOptions say how much a manifest is trusted.
type ParseOptions struct {
	Bundled bool // lucode.* ids are reserved for the plugins in the binary
}

// ParseManifest reads and validates plugin.json. Permissions a host does not know are an
// error, so nothing a plugin asks for goes unseen by the admin reviewing it.
func ParseManifest(raw []byte, opts ParseOptions) (Manifest, error) {
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("plugin.json: %w", err)
	}
	if err := m.normalize(); err != nil {
		return m, err
	}
	return m, m.validate(opts)
}

func (m *Manifest) normalize() error {
	if m.Targets.Server != nil && m.Main != "" {
		return errors.New(`plugin.json: use "targets" or "main", not both`)
	}
	if m.Main != "" {
		m.Targets.Server = &Target{Main: m.Main}
	}
	if m.Permissions != nil {
		if m.Targets.Server == nil {
			return errors.New(`plugin.json: top-level "permissions" are server permissions, and there is no server target`)
		}
		if m.Main == "" && !isZero(m.Targets.Server.Permissions) {
			return errors.New(`plugin.json: permissions are given twice for the server target`)
		}
		m.Targets.Server.Permissions = *m.Permissions
	}
	m.Main, m.Permissions = "", nil
	for i := range m.Contributes.FlowNodes {
		n := &m.Contributes.FlowNodes[i]
		if n.Target == "" {
			n.Target = TargetServer
		}
		if !contains(n.Ports, "failed") {
			n.Ports = append(n.Ports, "failed") // every plugin node can fail
		}
	}
	for _, list := range [][]ActionDef{m.Contributes.ChatActions, m.Contributes.CardActions} {
		for i := range list {
			if list[i].Target == "" {
				list[i].Target = TargetServer
			}
		}
	}
	return nil
}

func isZero(p Permissions) bool {
	b, _ := json.Marshal(p)
	return string(b) == "{}"
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (m *Manifest) validate(opts ParseOptions) error {
	if !idRe.MatchString(m.ID) || len(m.ID) > 100 {
		return fmt.Errorf("plugin.json: id %q must look like acme.hello", m.ID)
	}
	if strings.HasPrefix(m.ID, "lucode.") && !opts.Bundled {
		return fmt.Errorf("plugin.json: the id %q is reserved: lucode.* is for plugins that ship with lucode", m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return errors.New("plugin.json: name is required")
	}
	if !semverRe.MatchString(m.Version) {
		return fmt.Errorf("plugin.json: version %q is not semver", m.Version)
	}
	if m.SDK != 1 {
		return fmt.Errorf("plugin.json: sdk %d is not supported by this lucode (it knows sdk 1)", m.SDK)
	}
	if m.Homepage != "" {
		if u, err := url.Parse(m.Homepage); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("plugin.json: homepage must be an http or https address")
		}
	}
	if m.Targets.Server == nil && m.Targets.Runner == nil {
		return errors.New(`plugin.json: no code: give "targets" with a server or a runner`)
	}
	for _, c := range []struct {
		name string
		t    *Target
	}{{TargetServer, m.Targets.Server}, {TargetRunner, m.Targets.Runner}} {
		if c.t == nil {
			continue
		}
		if err := checkPluginPath(c.t.Main); err != nil || path.Ext(c.t.Main) != ".js" {
			return fmt.Errorf("plugin.json: the main file of target %s must be a .js file in the zip", c.name)
		}
		if err := checkPermissions(c.name, c.t.Permissions); err != nil {
			return err
		}
	}
	if err := m.checkContributes(); err != nil {
		return err
	}
	return m.checkBuild()
}

func checkPermissions(target string, p Permissions) error {
	if target == TargetServer {
		if p.FS != "" || p.Git != "" || p.Exec != nil {
			return errors.New("plugin.json: fs, git and exec are runner permissions")
		}
		if p.Sessions != "" && p.Sessions != "read" {
			return errors.New(`plugin.json: permission sessions can only be "read"`)
		}
		if p.Board != "" && p.Board != "read" && p.Board != "write" {
			return errors.New(`plugin.json: permission board must be "read" or "write"`)
		}
		if p.Browser != nil {
			if len(p.Browser.Origins) == 0 {
				return errors.New("plugin.json: permission browser lists no origins")
			}
			for _, o := range p.Browser.Origins {
				if !validOrigin(o) {
					return fmt.Errorf("plugin.json: %q is not an https origin", o)
				}
			}
		}
		if p.Relay != nil {
			if len(p.Relay.Providers) == 0 {
				return errors.New("plugin.json: permission relay lists no providers")
			}
			for _, name := range p.Relay.Providers {
				if !providerRe.MatchString(name) {
					return fmt.Errorf("plugin.json: %q is not a provider name (a-z, 0-9, - and _)", name)
				}
			}
		}
	} else {
		if p.Storage || p.Conversations || p.Sessions != "" || p.Board != "" || p.Flows || p.Browser != nil || p.Relay != nil {
			return errors.New("plugin.json: storage, conversations, sessions, board, flows, browser and relay are server permissions")
		}
		if p.FS != "" && p.FS != "read" && p.FS != "write" {
			return errors.New(`plugin.json: permission fs must be "read" or "write"`)
		}
		if p.Git != "" && p.Git != "read" {
			return errors.New(`plugin.json: permission git can only be "read"`)
		}
		if p.Exec != nil {
			if len(p.Exec.Commands) == 0 {
				return errors.New("plugin.json: permission exec lists no commands")
			}
			for _, c := range p.Exec.Commands {
				if !commandRe.MatchString(c) {
					return fmt.Errorf("plugin.json: %q is not a command name: exec takes base names, not paths", c)
				}
			}
		}
	}
	if p.HTTP != nil {
		if len(p.HTTP.Hosts) == 0 {
			return errors.New("plugin.json: permission http lists no hosts")
		}
		for _, h := range p.HTTP.Hosts {
			if !hostRe.MatchString(h) {
				return fmt.Errorf("plugin.json: %q is not a host name (an address, a port or a bare * cannot be allowed)", h)
			}
		}
	}
	return nil
}

func validOrigin(o string) bool {
	u, err := url.Parse(o)
	if err != nil || u.Scheme != "https" || u.Path != "" || u.RawQuery != "" || u.User != nil {
		return false
	}
	return hostRe.MatchString(u.Hostname()) && !strings.HasPrefix(u.Hostname(), "*.")
}

func checkIcon(where, p string) error {
	if p == "" {
		return nil
	}
	if err := checkPluginPath(p); err != nil || !isImage(p) {
		return fmt.Errorf("plugin.json: the icon of %s must be an image in the zip", where)
	}
	return nil
}

// checkPage: a page is drawn one way, view trees or a frontend, and a frontend names every rpc it
// may call, because that list is all the browser lets it reach.
func checkPage(d PageDef) error {
	if d.Placement != "" && d.Placement != "settings" {
		return fmt.Errorf(`plugin.json: page %q: placement %q must be "settings" or left out`, d.ID, d.Placement)
	}
	switch {
	case d.Render != "" && d.UI != "":
		return fmt.Errorf(`plugin.json: page %q: use "render" or "ui", not both`, d.ID)
	case d.UI != "":
		if err := checkPluginPath(d.UI); err != nil || path.Ext(d.UI) != ".js" {
			return fmt.Errorf("plugin.json: page %q: ui %q must be a .js file in the zip", d.ID, d.UI)
		}
		if len(d.RPC) > MaxPageRPCs {
			return fmt.Errorf("plugin.json: page %q lists %d rpcs, and %d is the most", d.ID, len(d.RPC), MaxPageRPCs)
		}
		seen := map[string]bool{}
		for _, name := range d.RPC {
			if !rpcNameRe.MatchString(name) {
				return fmt.Errorf("plugin.json: page %q: rpc %q is not an rpc name", d.ID, name)
			}
			if seen[name] {
				return fmt.Errorf("plugin.json: page %q lists the rpc %q twice", d.ID, name)
			}
			seen[name] = true
		}
	case d.Render != "":
		if !rpcNameRe.MatchString(d.Render) {
			return fmt.Errorf("plugin.json: page %q: render %q is not an rpc name", d.ID, d.Render)
		}
		if len(d.RPC) > 0 {
			return fmt.Errorf(`plugin.json: page %q: "rpc" is for a page with "ui"; a view tree offers its rpcs as actions`, d.ID)
		}
	default:
		return fmt.Errorf(`plugin.json: page %q needs "render" (an rpc that returns a view tree) or "ui" (its frontend)`, d.ID)
	}
	return nil
}

func (m *Manifest) checkBuild() error {
	b := m.Build
	if b == nil {
		return nil
	}
	sources := []string{b.Server, b.Runner}
	if b.Server != "" && m.Targets.Server == nil {
		return errors.New(`plugin.json: build.server needs targets.server, which names where it goes`)
	}
	if b.Runner != "" && m.Targets.Runner == nil {
		return errors.New(`plugin.json: build.runner needs targets.runner, which names where it goes`)
	}
	for id, src := range b.Pages {
		found := false
		for _, p := range m.Contributes.Pages {
			if p.ID == id && p.UI != "" {
				found = true
			}
		}
		if !found {
			return fmt.Errorf(`plugin.json: build.pages names %q, which is not a page with "ui"`, id)
		}
		sources = append(sources, src)
	}
	for _, src := range sources {
		if src == "" {
			continue
		}
		if err := checkPluginPath(src); err != nil {
			return fmt.Errorf("plugin.json: build source %q must be a path inside the plugin's folder", src)
		}
	}
	return nil
}

func uniqueIDs(kind string, ids []string) error {
	seen := map[string]bool{}
	for _, id := range ids {
		if !localIDRe.MatchString(id) {
			return fmt.Errorf("plugin.json: %s id %q may only use a-z, 0-9, - and _", kind, id)
		}
		if seen[id] {
			return fmt.Errorf("plugin.json: %s id %q is there twice", kind, id)
		}
		seen[id] = true
	}
	return nil
}

func (m *Manifest) checkContributes() error {
	c := m.Contributes
	srv := m.Targets.Server != nil
	if err := checkIcon("the plugin", m.Icon); err != nil {
		return err
	}
	var ids []string
	for _, d := range c.Models {
		ids = append(ids, d.ID)
		if !srv {
			return errors.New("plugin.json: models need a server target")
		}
		if claudeIDRe.MatchString(d.ID) {
			return fmt.Errorf("plugin.json: the model id %q is reserved", d.ID)
		}
		if strings.TrimSpace(d.Label) == "" {
			return fmt.Errorf("plugin.json: model %q has no label", d.ID)
		}
		if d.TimeoutSeconds < 0 || d.TimeoutSeconds > MaxTimeoutSeconds {
			return fmt.Errorf("plugin.json: model %q: timeoutSeconds must be at most %d", d.ID, MaxTimeoutSeconds)
		}
		if d.Accent != "" && !accentRe.MatchString(d.Accent) {
			return fmt.Errorf("plugin.json: model %q: accent must be a #hex colour", d.ID)
		}
		if err := checkIcon("model "+d.ID, d.Icon); err != nil {
			return err
		}
	}
	if err := uniqueIDs("model", ids); err != nil {
		return err
	}
	ids = nil
	for _, d := range c.Conversations {
		ids = append(ids, d.ID)
		if !m.hasModel(d.Model) {
			return fmt.Errorf("plugin.json: conversation %q names the model %q, which this plugin does not have", d.ID, d.Model)
		}
		if !srv || !m.Targets.Server.Permissions.Conversations {
			return fmt.Errorf("plugin.json: conversation %q needs the conversations permission", d.ID)
		}
	}
	if err := uniqueIDs("conversation", ids); err != nil {
		return err
	}
	ids = nil
	for _, d := range c.Pages {
		ids = append(ids, d.ID)
		if !srv {
			return errors.New("plugin.json: pages need a server target")
		}
		if err := checkPage(d); err != nil {
			return err
		}
		if err := checkIcon("page "+d.ID, d.Icon); err != nil {
			return err
		}
	}
	if err := uniqueIDs("page", ids); err != nil {
		return err
	}
	seenKey := map[string]bool{}
	for _, s := range c.Settings {
		if !rpcNameRe.MatchString(s.Key) || seenKey[s.Key] {
			return fmt.Errorf("plugin.json: the setting key %q is not valid or is there twice", s.Key)
		}
		seenKey[s.Key] = true
		if !settingTypes[s.Type] {
			return fmt.Errorf("plugin.json: setting %q: unknown type %q", s.Key, s.Type)
		}
		if s.Type == "select" && len(s.Options) == 0 {
			return fmt.Errorf("plugin.json: setting %q is a select with no options", s.Key)
		}
		if !srv {
			return errors.New("plugin.json: settings need a server target: they never reach a runner by themselves")
		}
	}
	ids = nil
	for _, d := range c.Blueprints {
		ids = append(ids, d.Key)
		if err := checkPluginPath(d.File); err != nil || path.Ext(d.File) != ".json" || !strings.HasPrefix(d.File, "blueprints/") {
			return fmt.Errorf("plugin.json: blueprint %q: file must be blueprints/*.json", d.Key)
		}
		if !srv || !m.Targets.Server.Permissions.Flows {
			return fmt.Errorf("plugin.json: blueprint %q needs the flows permission", d.Key)
		}
		if strings.TrimSpace(d.Name) == "" {
			return fmt.Errorf("plugin.json: blueprint %q has no name", d.Key)
		}
	}
	if err := uniqueIDs("blueprint", ids); err != nil {
		return err
	}
	ids = nil
	for _, d := range c.FlowNodes {
		ids = append(ids, d.ID)
		if err := m.checkNode(d); err != nil {
			return err
		}
	}
	if err := uniqueIDs("flow node", ids); err != nil {
		return err
	}
	for _, list := range [][]ActionDef{c.ChatActions, c.CardActions} {
		ids = nil
		for _, d := range list {
			ids = append(ids, d.ID)
			if !rpcNameRe.MatchString(d.RPC) {
				return fmt.Errorf("plugin.json: action %q: rpc %q is not an rpc name", d.ID, d.RPC)
			}
			if err := m.checkTarget("action "+d.ID, d.Target); err != nil {
				return err
			}
			if err := checkIcon("action "+d.ID, d.Icon); err != nil {
				return err
			}
		}
		if err := uniqueIDs("action", ids); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manifest) checkTarget(what, target string) error {
	switch target {
	case TargetServer:
		if m.Targets.Server == nil {
			return fmt.Errorf("plugin.json: %s targets the server, and this plugin has no server target", what)
		}
	case TargetRunner:
		if m.Targets.Runner == nil {
			return fmt.Errorf("plugin.json: %s targets a runner, and this plugin has no runner target", what)
		}
	default:
		return fmt.Errorf("plugin.json: %s: unknown target %q", what, target)
	}
	return nil
}

func (m *Manifest) checkNode(d FlowNodeDef) error {
	where := "flow node " + d.ID
	if err := m.checkTarget(where, d.Target); err != nil {
		return err
	}
	if m.Targets.Server == nil || !m.Targets.Server.Permissions.Flows {
		return fmt.Errorf("plugin.json: %s needs the flows permission of the server target", where)
	}
	if strings.TrimSpace(d.Label) == "" {
		return fmt.Errorf("plugin.json: %s has no label", where)
	}
	if d.TimeoutSeconds < 0 || d.TimeoutSeconds > MaxTimeoutSeconds {
		return fmt.Errorf("plugin.json: %s: timeoutSeconds must be at most %d", where, MaxTimeoutSeconds)
	}
	for _, p := range d.Ports {
		if !portRe.MatchString(p) {
			return fmt.Errorf("plugin.json: %s: %q is not a port name", where, p)
		}
	}
	seen := map[string]bool{}
	for _, f := range d.Fields {
		if !rpcNameRe.MatchString(f.Name) || seen[f.Name] {
			return fmt.Errorf("plugin.json: %s: field name %q is not valid or is there twice", where, f.Name)
		}
		seen[f.Name] = true
		if !nodeFieldKinds[f.Kind] {
			return fmt.Errorf("plugin.json: %s: field %q has the unknown kind %q", where, f.Name, f.Kind)
		}
		if f.PortLabel != "" && !contains(d.Ports, f.PortLabel) {
			return fmt.Errorf("plugin.json: %s: field %q labels the port %q, which the node does not have", where, f.Name, f.PortLabel)
		}
	}
	return checkIcon(where, d.Icon)
}

func (m *Manifest) hasModel(id string) bool {
	_, ok := m.Model(id)
	return ok
}

// Model finds a model of the plugin.
func (m *Manifest) Model(id string) (ModelDef, bool) {
	for _, d := range m.Contributes.Models {
		if d.ID == id {
			return d, true
		}
	}
	return ModelDef{}, false
}

// Node finds a flow node of the plugin.
func (m *Manifest) Node(id string) (FlowNodeDef, bool) {
	for _, d := range m.Contributes.FlowNodes {
		if d.ID == id {
			return d, true
		}
	}
	return FlowNodeDef{}, false
}

// Target returns the named target, or nil.
func (m *Manifest) Target(name string) *Target {
	if name == TargetRunner {
		return m.Targets.Runner
	}
	return m.Targets.Server
}

// PermissionInfo is one line of the review dialog. Key is what a grant names.
type PermissionInfo struct {
	Key    string `json:"key"`
	Target string `json:"target"`
	Label  string `json:"label"`
	Risk   string `json:"risk"` // low, medium or high
	Detail string `json:"detail"`
}

// PermissionsOf lists everything a manifest asks for, per target. Hosts, origins and commands
// have a key each, so an upgrade that adds one needs a new grant.
func PermissionsOf(m Manifest) []PermissionInfo {
	out := []PermissionInfo{}
	add := func(target, key, label, risk, detail string) {
		out = append(out, PermissionInfo{Key: target + "." + key, Target: target, Label: label, Risk: risk, Detail: detail})
	}
	if t := m.Targets.Server; t != nil {
		p := t.Permissions
		if p.Storage {
			add(TargetServer, "storage", "Own storage", "low", "Keeps up to 10 MB of its own data on the hub.")
		}
		if p.Conversations {
			add(TargetServer, "conversations", "Conversations", "low", "Keeps its own list of conversations.")
		}
		if p.Sessions != "" {
			add(TargetServer, "sessions:read", "Read chats", "medium", "Reads the messages of the chats of the project it is called in.")
		}
		if p.Board != "" {
			add(TargetServer, "board:read", "Read the board", "low", "Reads lists and cards of the project it is called in.")
		}
		if p.Board == "write" {
			add(TargetServer, "board:write", "Change the board", "medium", "Creates, moves, comments on and tags cards of the project it is called in.")
		}
		if p.Flows {
			add(TargetServer, "flows", "Flows", "medium", "Adds nodes and blueprints to flows and fires flow triggers.")
		}
		if p.HTTP != nil {
			for _, h := range p.HTTP.Hosts {
				add(TargetServer, "http:"+h, "Network from the hub: "+h, "medium", "Sends requests to this host over https from the hub.")
			}
		}
		if p.Relay != nil {
			for _, name := range p.Relay.Providers {
				add(TargetServer, "relay:"+name, "Webhooks from "+name+" through the relay", "medium", "Gets public addresses on the hub's webhook relay for "+name+" and receives what that service posts to them, such as the messages people send. It also picks the project those events belong to.")
			}
		}
		if p.Browser != nil {
			for _, o := range p.Browser.Origins {
				add(TargetServer, "browser:"+o, "Browser on "+o, "high", "Drives its own Edge on the hub and reads and runs code in pages of this origin. Code running in a page can still send what it reads anywhere: this limits what the plugin itself reads, not where a page sends data.")
			}
		}
	}
	if t := m.Targets.Runner; t != nil {
		p := t.Permissions
		if p.FS != "" {
			add(TargetRunner, "fs:read", "Read workspace files", "medium", "Reads files of the workspace on the runner. Secret files and .git are refused.")
		}
		if p.FS == "write" {
			add(TargetRunner, "fs:write", "Write workspace files", "high", "Writes files of the workspace on the runner. Secret files and .git are refused.")
		}
		if p.Git != "" {
			add(TargetRunner, "git:read", "Read git state", "medium", "Reads status, diff, log and branch of the workspace on the runner.")
		}
		if p.HTTP != nil {
			for _, h := range p.HTTP.Hosts {
				add(TargetRunner, "http:"+h, "Network from the runner: "+h, "medium", "Sends requests to this host over https from the runner.")
			}
		}
		if p.Exec != nil {
			for _, c := range p.Exec.Commands {
				add(TargetRunner, "exec:"+c, "Run the command "+c, "high", "Starts this program on the runner, in the workspace, with no shell. Only runners started with --plugin-policy=full allow it.")
			}
		}
	}
	return out
}

// Effective cuts a target's permissions down to what the grant covers. A target that asks for
// more than was granted simply does not get it.
func Effective(target string, p Permissions, granted []string) Permissions {
	has := func(key string) bool { return contains(granted, target+"."+key) }
	var out Permissions
	out.Storage = p.Storage && has("storage")
	out.Conversations = p.Conversations && has("conversations")
	out.Flows = p.Flows && has("flows")
	if p.Sessions != "" && has("sessions:read") {
		out.Sessions = "read"
	}
	if p.Board != "" && has("board:read") {
		out.Board = "read"
		if p.Board == "write" && has("board:write") {
			out.Board = "write"
		}
	}
	if p.FS != "" && has("fs:read") {
		out.FS = "read"
		if p.FS == "write" && has("fs:write") {
			out.FS = "write"
		}
	}
	if p.Git != "" && has("git:read") {
		out.Git = "read"
	}
	if p.HTTP != nil {
		var hosts []string
		for _, h := range p.HTTP.Hosts {
			if has("http:" + h) {
				hosts = append(hosts, h)
			}
		}
		if hosts != nil {
			out.HTTP = &HTTPPerm{Hosts: hosts}
		}
	}
	if p.Relay != nil {
		var names []string
		for _, name := range p.Relay.Providers {
			if has("relay:" + name) {
				names = append(names, name)
			}
		}
		if names != nil {
			out.Relay = &RelayPerm{Providers: names}
		}
	}
	if p.Browser != nil {
		var origins []string
		for _, o := range p.Browser.Origins {
			if has("browser:" + o) {
				origins = append(origins, o)
			}
		}
		if origins != nil {
			out.Browser = &BrowserPerm{Origins: origins}
		}
	}
	if p.Exec != nil {
		var cmds []string
		for _, c := range p.Exec.Commands {
			if has("exec:" + c) {
				cmds = append(cmds, c)
			}
		}
		if cmds != nil {
			out.Exec = &ExecPerm{Commands: cmds}
		}
	}
	return out
}

// Model ids on the wire.
const (
	ModelPrefix = "plugin:"
	// LegacyAIMode is the id old sessions and saved choices have.
	LegacyAIMode = "local:aimode"
	// MappedAIMode is what LegacyAIMode means.
	MappedAIMode = "plugin:lucode.gemini/aimode"
	// GeminiID is the plugin of that model.
	GeminiID = "lucode.gemini"
)

// WireModel is the id of a plugin's model on the wire: the plugin's id comes from the host, so
// a manifest cannot claim a model for another plugin.
func WireModel(pluginID, modelID string) string { return ModelPrefix + pluginID + "/" + modelID }

// ParseWireModel splits plugin:<plugin>/<model>.
func ParseWireModel(s string) (pluginID, modelID string, ok bool) {
	rest, found := strings.CutPrefix(s, ModelPrefix)
	if !found {
		return "", "", false
	}
	pluginID, modelID, ok = strings.Cut(rest, "/")
	if !ok || !idRe.MatchString(pluginID) || !localIDRe.MatchString(modelID) {
		return "", "", false
	}
	return pluginID, modelID, true
}

// IsPluginModel reports whether a model id asks for a plugin, valid or not.
func IsPluginModel(s string) bool { return strings.HasPrefix(s, ModelPrefix) }
