package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io/fs"

	"github.com/nats-io/nats.go"
)

// The hub's host lives in internal/plugins/hub. These are the types the API and the host share,
// here so that internal/api (which lucode-runner also links) does not import the host.

// ErrNotFound means no plugin with that id is installed.
var ErrNotFound = errors.New("plugin not found")

// ErrRPCRefused means the browser asked for a call the plugin does not offer it.
var ErrRPCRefused = errors.New("this call is not offered")

// Cause says who changed the board.
type Cause struct {
	Source string
	FlowID string
	RunID  string
	Depth  int
}

// Services are the hub's own rules and side effects, which a plugin goes through like anyone else.
type Services interface {
	CreateCard(projectID, title, description, status, agent string, cause Cause) (string, error)
	MoveCard(cardID, kind, listID string, cause Cause) (rejected string, err error)
	CommentCard(cardID, author, text string) error
	AddCardTag(cardID, tag string) error
	Broadcast(sessionID string, ev map[string]any)
	NotifyFlows()
	// Workspace says where work on a session or card happens: its runner and folder there. The
	// hub decides it. A runner id narrows it to that runner; "" takes the one the work is on.
	Workspace(projectID, sessionID, cardID, runnerID string) (runner, dir string, err error)
	// ValidateGraph lists what stops a flow graph from being published.
	ValidateGraph(raw json.RawMessage) []string
}

// Peer is a runner the hub can see.
type Peer struct {
	ID       string
	Hostname string
	Policy   string
	Version  string
}

// BrowserDriver is one Edge of one plugin. Tests use a fake.
type BrowserDriver interface {
	Launch(headless bool) (string, error)
	Close() string
	Status() string
	Tabs() (any, error)
	NewTab(url string) (any, error)
	SwitchTab(id string) (any, error)
	CloseTab(id string) error
	URL() (string, error)
	Info() (any, error)
	Navigate(url string) (any, error)
	Find(role, name, selector string, exact bool, limit int) (any, error)
	Click(ref string) (any, error)
	Type(ref, text string, submit, clear bool) (any, error)
	Press(combo string) (any, error)
	Eval(js string) (string, error)
	Text(ref string, max int) (string, error)
}

// RelayEvent is one webhook the relay hands to this hub: what a service posted to an address the
// hub asked the relay for.
type RelayEvent struct {
	Seq         uint64
	Endpoint    string
	Provider    string
	ReceivedAt  int64 // milliseconds since the epoch
	ContentType string
	Body        string
}

// RelayEndpoint is an address on the relay. Secret is only there on the answer to a create: the
// relay shows it once, and the plugin gives it to the service (Telegram's secret_token).
type RelayEndpoint struct {
	ID       string
	Provider string
	Label    string
	URL      string
	Secret   string
}

// RelayStatus says whether the hub has a relay and is talking to it. It never carries the key.
type RelayStatus struct {
	Configured bool
	Connected  bool
	URL        string
}

// RelayClient is the hub's connection to the webhook relay (internal/relayclient). Tests use a fake.
type RelayClient interface {
	Status() RelayStatus
	CreateEndpoint(ctx context.Context, provider, label string) (RelayEndpoint, error)
	DeleteEndpoint(ctx context.Context, id string) error
	// SetHandler says who gets the events. The client acknowledges an event to the relay once the
	// handler has returned, so the relay keeps what the hub has not handled yet.
	SetHandler(func(ctx context.Context, ev RelayEvent))
}

// HubDeps is what the hub gives the host.
type HubDeps struct {
	DB          *sql.DB
	DataDir     string
	Services    Services
	Peers       func() []Peer
	Relay       RelayClient                         // nil: no webhooks
	NATS        *nats.Conn                          // nil: no runners
	Browser     func(pluginID string) BrowserDriver // nil: the real Edge
	HTTP        HTTPOptions
	Bundled     fs.FS    // nil: what ships in the binary
	AutoInstall []string // bundled ids installed on first start
	MaxDepth    int
}

// HubFactory builds the hub's host. cmd/lucode names it, so nothing else links the host.
type HubFactory func(HubDeps) (HubHost, error)

// PluginView is an installed plugin as the API shows it.
type PluginView struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Version  string          `json:"version"`
	Manifest Manifest        `json:"manifest"`
	Status   string          `json:"status"` // enabled, disabled or error
	Enabled  bool            `json:"enabled"`
	Granted  []string        `json:"granted"`
	Error    string          `json:"error"`
	Source   string          `json:"source"`
	Targets  map[string]any  `json:"targets"`
	Settings json.RawMessage `json:"-"`
}

// InspectResult is what the review dialog shows.
type InspectResult struct {
	Token          string           `json:"token"`
	Manifest       Manifest         `json:"manifest"`
	Permissions    []PermissionInfo `json:"permissions"`
	UpgradeFrom    string           `json:"upgradeFrom,omitempty"`
	NewPermissions []string         `json:"newPermissions"`
	Warnings       []string         `json:"warnings"`
}

// CatalogEntry is a plugin that ships with lucode.
type CatalogEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Installed   bool   `json:"installed"`
}

// UIContributions are the enabled plugins' additions to the web. Each entry keeps the manifest's
// fields and gains pluginId and pluginName.
type UIContributions struct {
	Models        []map[string]any `json:"models"`
	Conversations []map[string]any `json:"conversations"`
	Pages         []map[string]any `json:"pages"`
	ChatActions   []map[string]any `json:"chatActions"`
	CardActions   []map[string]any `json:"cardActions"`
}

// TurnRequest is a chat message for a plugin's model.
type TurnRequest struct {
	SessionID      string
	ProjectID      string
	Message        string
	Model          string
	ImageBase64    string
	ImageMediaType string
}

// NodeInfo is a flow node of an installed plugin.
type NodeInfo struct {
	Type       string
	PluginID   string
	PluginName string
	Disabled   bool
	Def        FlowNodeDef
}

// BlueprintInfo is a blueprint of an installed plugin.
type BlueprintInfo struct {
	PluginID   string
	PluginName string
	Def        BlueprintDef
	Graph      json.RawMessage
}

// NodeCall runs a plugin's flow node.
type NodeCall struct {
	Type      string
	Config    map[string]any
	Card      map[string]any
	Event     map[string]any
	Input     any // the value that reached the node
	Run       map[string]any
	ProjectID string
	CardID    string
	FlowID    string
	RunID     string
	Depth     int
}

// RunnerRow is one line of GET /api/plugins/{id}/runners.
type RunnerRow struct {
	RunnerID  string `json:"runnerId"`
	Hostname  string `json:"hostname"`
	Online    bool   `json:"online"`
	Policy    string `json:"policy"`
	Paired    bool   `json:"paired"`
	Delivered bool   `json:"delivered"`
	Version   string `json:"version"`
	Error     string `json:"error"`
}

// HubHost is the hub's plugin host, as the API sees it.
type HubHost interface {
	Start(ctx context.Context) error
	List() []PluginView
	Get(id string) (PluginView, error)
	Inspect(zipFile string) (InspectResult, error)
	Install(token string, granted []string) (PluginView, error)
	Catalog() []CatalogEntry
	InstallBundled(id string) (PluginView, error)
	SetEnabled(id string, enabled bool) (PluginView, error)
	Uninstall(id string, keepData bool) error
	Settings(id string) (map[string]any, error)
	PutSettings(id string, values map[string]any) error
	Logs(id string) ([]LogEntry, error)
	Asset(id, rel string) ([]byte, string, error)
	UI() UIContributions
	RPC(ctx context.Context, id, name, projectID string, args any) (any, error)
	CheckModel(wire string) error
	Turn(ctx context.Context, req TurnRequest, progress func(string)) (string, error)
	FlowNodes() []NodeInfo
	Blueprints() []BlueprintInfo
	RunNode(ctx context.Context, call NodeCall) (port string, output map[string]any, err error)
	SeedProject(projectID string)
	Runners(id string) ([]RunnerRow, error)
	// PairRunner makes a new secret for a runner, which also revokes the old one.
	PairRunner(runnerID string) (code, command string, err error)
	DeliverEvent(name, projectID string, payload map[string]any, depth int)
	SessionDeleted(projectID, sessionID string)
	// SessionClosed tells the plugins with a conversation in that session that it is over: it
	// was deleted or replaced. It never blocks the caller.
	SessionClosed(projectID, sessionID string)
}
