package plugins

// What hub and runner say to each other over NATS. A package goes in chunks, because a NATS
// message is small: the signed delivery names its sha256, and the runner fetches and checks it.

// Runner policies, from --plugin-policy.
const (
	PolicyOff      = "off"
	PolicyStandard = "standard"
	PolicyFull     = "full"
)

// ValidPolicy reports whether a policy exists.
func ValidPolicy(p string) bool { return p == PolicyOff || p == PolicyStandard || p == PolicyFull }

// DiscoveryPolicyKey is the field of a runner's discovery info that says its policy.
const DiscoveryPolicyKey = "plugin_policy"

// ChunkBytes is how much of a package travels in one message.
const ChunkBytes = 512 << 10

// MaxWireBytes is what fits in one NATS message with room to spare, sealed and encoded.
const MaxWireBytes = 900 << 10

// RunnerSubject is where a runner listens for signed messages.
func RunnerSubject(runnerID string) string { return "lucode.plugin.runner." + runnerID }

// PackageSubject is where a hub serves a package's chunks.
func PackageSubject(sha string) string { return "lucode.plugin.package." + sha }

// DeliverPayload announces a package.
type DeliverPayload struct {
	PluginID string `json:"plugin_id"`
	Version  string `json:"version"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
}

// ChunkRequest asks for part of a package.
type ChunkRequest struct {
	Offset int64 `json:"offset"`
}

// InvokePayload runs code on a runner. The hub decides the workspace and the grant; the plugin
// has no say in them.
type InvokePayload struct {
	ID        string   `json:"id"`
	PluginID  string   `json:"plugin_id"`
	SHA256    string   `json:"sha256"`
	Granted   []string `json:"granted"`
	Path      []string `json:"path"`
	Arg       any      `json:"arg"`
	TimeoutMS int64    `json:"timeout_ms"`
	Context   Context  `json:"context"`
	Workspace string   `json:"workspace"`
	// CallSubject and CallToken let this invocation reach its own plugin's server rpcs.
	CallSubject string `json:"call_subject,omitempty"`
	CallToken   string `json:"call_token,omitempty"`
}

// CancelPayload stops an invocation.
type CancelPayload struct {
	ID string `json:"id"`
}

// UnloadPayload removes a plugin from a runner.
type UnloadPayload struct {
	PluginID string `json:"plugin_id"`
}

// ServerCall is lucode.server.call from a runner.
type ServerCall struct {
	TS    int64  `json:"ts"` // unix ms
	Nonce string `json:"nonce"`
	Token string `json:"token"`
	RPC   string `json:"rpc"`
	Args  any    `json:"args"`
}

// RunnerReply answers any message to a runner.
type RunnerReply struct {
	OK    bool   `json:"ok"`
	Code  string `json:"code,omitempty"`
	Error string `json:"error,omitempty"`
	Value any    `json:"value,omitempty"`
	// lines the plugin logged on the runner, for its log on the hub
	Logs []LogEntry `json:"logs,omitempty"`
}

// PackageChunk is a sealed part of a package. An empty box means the hub no longer offers it.
type PackageChunk struct {
	Box string `json:"box"`
}

// RunnerStatus is what a runner says about itself and a plugin.
type RunnerStatus struct {
	Policy    string `json:"policy"`
	Delivered bool   `json:"delivered"`
	Version   string `json:"version"`
}
