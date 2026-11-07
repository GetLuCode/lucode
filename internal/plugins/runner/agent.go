// Package runner is the plugin host of lucode-runner. It takes signed packages and signed
// invocations from the hub it pinned, over NATS only, and runs them in the same sandbox as the
// hub, with fs, git and exec added under the operator's policy.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"claude-orchestrator/internal/plugins"

	"github.com/nats-io/nats.go"
)

var pluginIDRe = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9-]+)+$`)
var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

const (
	chunkTimeout   = 30 * time.Second
	maxLogsInReply = 50
)

// Options make an Agent.
type Options struct {
	NATS     *nats.Conn
	RunnerID string
	DataDir  string
	Policy   string
	HTTP     plugins.HTTPOptions // tests
}

// Agent is the runner's plugin host.
type Agent struct {
	o      Options
	root   string
	verify *plugins.Verifier

	mu      sync.Mutex
	loaded  map[string]*loadedPlugin
	running map[string]context.CancelFunc
}

type loadedPlugin struct {
	sha     string
	version string
	rt      *plugins.Runtime
	logSeq  int
}

// New reads what was delivered earlier and makes the agent. It runs nothing until a signed
// invocation asks for it.
func New(o Options) (*Agent, error) {
	if !plugins.ValidPolicy(o.Policy) {
		return nil, fmt.Errorf("unknown plugin policy %q: use off, standard or full", o.Policy)
	}
	root := filepath.Join(o.DataDir, "plugins")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	a := &Agent{
		o: o, root: root, verify: plugins.NewVerifier(o.RunnerID, o.DataDir),
		loaded: map[string]*loadedPlugin{}, running: map[string]context.CancelFunc{},
	}
	if o.Policy != plugins.PolicyOff {
		a.loadFromDisk()
	}
	return a, nil
}

// Start listens for the hub. Under policy off it still answers, to say no.
func (a *Agent) Start() error {
	if _, err := a.o.NATS.Subscribe(plugins.RunnerSubject(a.o.RunnerID), func(m *nats.Msg) { go a.serve(m) }); err != nil {
		return err
	}
	return a.o.NATS.Flush()
}

func (a *Agent) loadFromDisk() {
	entries, err := os.ReadDir(a.root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || !pluginIDRe.MatchString(e.Name()) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(a.root, e.Name(), "current"))
		if err != nil {
			continue
		}
		sha := strings.TrimSpace(string(raw))
		if !shaRe.MatchString(sha) {
			continue
		}
		if err := a.loadPlugin(e.Name(), sha); err != nil {
			log.Printf("[plugins] %s: %v", e.Name(), err)
		}
	}
}

func (a *Agent) packageDir(id, sha string) string { return filepath.Join(a.root, id, sha[:16]) }

func (a *Agent) loadPlugin(id, sha string) error {
	dir := a.packageDir(id, sha)
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return err
	}
	m, err := plugins.ParseManifest(raw, plugins.ParseOptions{Bundled: true})
	if err != nil {
		return err
	}
	if m.ID != id || m.Targets.Runner == nil {
		return errors.New("this package is not a runner plugin of that id")
	}
	rt := plugins.NewRuntime(plugins.Options{
		Package: &plugins.Package{Dir: dir, Manifest: m, SHA256: sha}, Target: plugins.TargetRunner, HTTP: a.o.HTTP,
	})
	a.mu.Lock()
	old := a.loaded[id]
	a.loaded[id] = &loadedPlugin{sha: sha, version: m.Version, rt: rt}
	a.mu.Unlock()
	if old != nil {
		old.rt.Close()
	}
	return nil
}

func (a *Agent) serve(m *nats.Msg) {
	_ = m.Respond(a.answer(m.Data))
}

// answer is everything that goes back on the wire. With a pairing it is sealed to the request; a
// refusal before that has no key to seal with, and the hub reads it as a refusal and nothing more.
func (a *Agent) answer(raw []byte) []byte {
	reply, secret, nonce := a.handle(raw)
	if secret == nil {
		out, _ := json.Marshal(plugins.Sealed{Runner: a.o.RunnerID, Code: reply.Code, Error: reply.Error})
		return out
	}
	out, err := plugins.SealReply(secret, a.o.RunnerID, nonce, reply)
	if err != nil || len(out) > plugins.MaxWireBytes {
		out, _ = plugins.SealReply(secret, a.o.RunnerID, nonce, plugins.RunnerReply{
			Error: fmt.Sprintf("this answer is larger than %d KB, the most that reaches the hub at once", plugins.MaxWireBytes>>10),
		})
	}
	return out
}

func refuse(err error) plugins.RunnerReply {
	var r *plugins.Refusal
	if errors.As(err, &r) {
		return plugins.RunnerReply{Code: r.Code, Error: r.Msg}
	}
	return plugins.RunnerReply{Error: err.Error()}
}

// handle returns the answer, and the secret and nonce to seal it with once the message is proved
// to come from the paired hub. Until then nothing is sealed, and nothing is said but no.
func (a *Agent) handle(raw []byte) (plugins.RunnerReply, []byte, string) {
	env, pair, err := a.verify.Open(raw)
	if err != nil {
		log.Printf("[plugins] refused a message: %v", err)
		reply := refuse(err)
		if a.o.Policy == plugins.PolicyOff {
			reply = plugins.RunnerReply{Code: plugins.RefusePolicy, Error: "this runner refuses plugins (--plugin-policy=off)"}
		} else if reply.Code != plugins.RefuseNotPaired {
			reply.Code = plugins.RefuseNotPaired // what can be said without a key
		}
		return reply, nil, ""
	}
	return a.act(env, pair), pair.Secret, env.Nonce
}

func (a *Agent) act(env plugins.Envelope, pair plugins.Pairing) plugins.RunnerReply {
	switch env.Kind {
	case plugins.KindStatus:
		var p plugins.UnloadPayload
		_ = json.Unmarshal(env.Payload, &p)
		a.mu.Lock()
		l := a.loaded[p.PluginID]
		a.mu.Unlock()
		st := plugins.RunnerStatus{Policy: a.o.Policy}
		if l != nil {
			st.Delivered, st.Version = true, l.version
		}
		return plugins.RunnerReply{OK: true, Value: st}
	case plugins.KindCancel:
		var p plugins.CancelPayload
		_ = json.Unmarshal(env.Payload, &p)
		a.mu.Lock()
		cancel := a.running[p.ID]
		a.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return plugins.RunnerReply{OK: true}
	case plugins.KindUnload:
		var p plugins.UnloadPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil || !pluginIDRe.MatchString(p.PluginID) {
			return plugins.RunnerReply{Error: "this unload cannot be read"}
		}
		a.unload(p.PluginID)
		return plugins.RunnerReply{OK: true}
	}
	if a.o.Policy == plugins.PolicyOff {
		return plugins.RunnerReply{Code: plugins.RefusePolicy, Error: "this runner refuses plugins (--plugin-policy=off)"}
	}
	switch env.Kind {
	case plugins.KindDeliver:
		var p plugins.DeliverPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return plugins.RunnerReply{Error: "this delivery cannot be read"}
		}
		if err := a.deliver(p, pair); err != nil {
			return plugins.RunnerReply{Error: plugins.Scrub(err.Error())}
		}
		return plugins.RunnerReply{OK: true}
	case plugins.KindInvoke:
		var p plugins.InvokePayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return plugins.RunnerReply{Error: "this invocation cannot be read"}
		}
		return a.invoke(p, pair)
	}
	return plugins.RunnerReply{Error: "unknown message"}
}

func (a *Agent) unload(id string) {
	a.mu.Lock()
	l := a.loaded[id]
	delete(a.loaded, id)
	a.mu.Unlock()
	if l != nil {
		l.rt.Close()
	}
	_ = os.RemoveAll(filepath.Join(a.root, id))
}

// deliver fetches a package, checks it against the sha256 the hub signed, validates it like any
// upload and swaps it in.
func (a *Agent) deliver(p plugins.DeliverPayload, pair plugins.Pairing) error {
	if !pluginIDRe.MatchString(p.PluginID) || !shaRe.MatchString(p.SHA256) {
		return errors.New("this delivery names an invalid plugin or package")
	}
	if p.Size <= 0 || p.Size > plugins.MaxZipBytes {
		return fmt.Errorf("this package is larger than %d MB", plugins.MaxZipBytes>>20)
	}
	a.mu.Lock()
	cur := a.loaded[p.PluginID]
	a.mu.Unlock()
	if cur != nil && cur.sha == p.SHA256 {
		return nil
	}
	dir := filepath.Join(a.root, p.PluginID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "incoming-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	err = a.fetch(tmp, p, pair)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	sha, err := plugins.FileSHA256(tmp.Name())
	if err != nil {
		return err
	}
	if sha != p.SHA256 {
		return errors.New("the package that arrived is not the one the hub signed")
	}
	// signed by the hub this runner pinned, so reserved ids are fine; the rest is checked again
	m, err := plugins.CheckZip(tmp.Name(), plugins.ParseOptions{Bundled: true})
	if err != nil {
		return err
	}
	if m.ID != p.PluginID || m.Targets.Runner == nil {
		return errors.New("this package is not a runner plugin of that id")
	}
	dest := a.packageDir(p.PluginID, sha)
	_ = os.RemoveAll(dest)
	if err := plugins.ExtractZip(tmp.Name(), dest); err != nil {
		return err
	}
	if err := a.loadPlugin(p.PluginID, sha); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "current"), []byte(sha), 0o644); err != nil {
		return err
	}
	if cur != nil {
		_ = os.RemoveAll(a.packageDir(p.PluginID, cur.sha))
	}
	log.Printf("[plugins] delivered %s %s", p.PluginID, p.Version)
	return nil
}

func (a *Agent) fetch(out *os.File, p plugins.DeliverPayload, pair plugins.Pairing) error {
	var offset int64
	for offset < p.Size {
		req, err := plugins.SealChunkRequest(pair.Secret, a.o.RunnerID, p.SHA256, offset)
		if err != nil {
			return err
		}
		msg, err := a.o.NATS.Request(plugins.PackageSubject(p.SHA256), req, chunkTimeout)
		if err != nil {
			return fmt.Errorf("fetching the package: %w", err)
		}
		var chunk plugins.PackageChunk
		if err := json.Unmarshal(msg.Data, &chunk); err != nil || chunk.Box == "" {
			return errors.New("the hub no longer offers this package")
		}
		data, err := plugins.OpenChunk(pair.Secret, a.o.RunnerID, p.SHA256, offset, chunk.Box)
		if err != nil || len(data) == 0 {
			return errors.New("a part of the package did not come from the hub")
		}
		if _, err := out.Write(data); err != nil {
			return err
		}
		offset += int64(len(data))
		if offset > plugins.MaxZipBytes {
			return errors.New("the package is larger than announced")
		}
	}
	return nil
}

// clampToPolicy cuts a grant down to what the operator allows here. A grant can never exceed it.
func clampToPolicy(policy string, p plugins.Permissions) (plugins.Permissions, string) {
	why := ""
	if policy != plugins.PolicyFull {
		if p.Exec != nil {
			why = "lucode.exec is refused by this runner's policy (" + policy + "): it takes --plugin-policy=full"
		}
		p.Exec = nil
	}
	return p, why
}

func (a *Agent) invoke(p plugins.InvokePayload, pair plugins.Pairing) plugins.RunnerReply {
	a.mu.Lock()
	l := a.loaded[p.PluginID]
	a.mu.Unlock()
	if l == nil || l.sha != p.SHA256 {
		return plugins.RunnerReply{Code: plugins.RefuseNotDelivered, Error: "this plugin was not delivered to this runner"}
	}
	m := l.rt.Manifest()
	perms, execRefusal := clampToPolicy(a.o.Policy, plugins.Effective(plugins.TargetRunner, m.Targets.Runner.Permissions, p.Granted))
	timeout := time.Duration(p.TimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > plugins.MaxTimeoutSeconds*time.Second {
		timeout = plugins.MaxTimeoutSeconds * time.Second
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.mu.Lock()
	a.running[p.ID] = cancel
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.running, p.ID)
		a.mu.Unlock()
	}()
	env := &invocationEnv{agent: a, perms: perms, execRefusal: execRefusal, payload: p, secret: pair.Secret}
	if err := env.checkWorkspace(); err != nil {
		return plugins.RunnerReply{Error: err.Error()}
	}
	v, err := l.rt.Invoke(ctx, plugins.Invocation{
		Path: p.Path, Arg: p.Arg, Timeout: timeout, Context: p.Context, Perms: perms,
		Modules: env.modules(),
	})
	logs := a.newLogs(l)
	if errors.Is(err, plugins.ErrNoHandler) {
		return plugins.RunnerReply{Code: "no_handler", Error: "the plugin has no handler for this on the runner", Logs: logs}
	}
	if err != nil {
		return plugins.RunnerReply{Error: plugins.Scrub(err.Error()), Logs: logs}
	}
	return plugins.RunnerReply{OK: true, Value: v, Logs: logs}
}

// newLogs are the lines written since the last answer, for the plugin's log on the hub.
func (a *Agent) newLogs(l *loadedPlugin) []plugins.LogEntry {
	a.mu.Lock()
	defer a.mu.Unlock()
	lines, seq := l.rt.LogsSince(l.logSeq)
	l.logSeq = seq
	if len(lines) > maxLogsInReply {
		lines = lines[len(lines)-maxLogsInReply:]
	}
	return lines
}
