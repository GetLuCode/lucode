package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"sync"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/plugins"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// The hub's side of the road to the runners. Every message is signed, for one runner. A runner
// fetches a package in chunks and checks it; to run code the hub must sign an invocation, which it
// only does for an enabled plugin, so a disabled one stops there even on a runner that was offline
// when it was disabled.

const (
	deliverEvery   = 20 * time.Second
	deliverTimeout = 3 * time.Minute
	statusTimeout  = 3 * time.Second
)

var runnerIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

type remote struct {
	h    *Host
	wake chan struct{}
	call string // the subject where this hub takes lucode.server.call

	mu        sync.Mutex
	delivered map[string]map[string]string // plugin -> runner -> sha256 it has
	failures  map[string]map[string]string // plugin -> runner -> last problem
	seen      map[string]plugins.Peer      // every runner seen, to show the ones now offline
	callsSeen map[string]time.Time         // lucode.server.call nonces, to refuse a replay
	tokens    map[string]*callToken
}

// callToken lets one running invocation reach its own plugin's server rpcs, until it ends.
type callToken struct {
	pluginID string
	runner   string
	ctx      context.Context
	context  plugins.Context
}

func newRemote(h *Host) *remote {
	return &remote{
		h: h, wake: make(chan struct{}, 1), call: "lucode.plugin.hubcall." + uuid.New().String(),
		delivered: map[string]map[string]string{}, failures: map[string]map[string]string{},
		seen: map[string]plugins.Peer{}, tokens: map[string]*callToken{}, callsSeen: map[string]time.Time{},
	}
}

func (r *remote) nc() *nats.Conn { return r.h.deps.NATS }

func (r *remote) start(ctx context.Context) error {
	nc := r.nc()
	if nc == nil {
		return nil
	}
	if _, err := nc.Subscribe("lucode.plugin.package.*", func(m *nats.Msg) { go r.servePackage(m) }); err != nil {
		return err
	}
	if _, err := nc.Subscribe(r.call, func(m *nats.Msg) { go r.serveCall(m) }); err != nil {
		return err
	}
	if err := nc.Flush(); err != nil {
		return err
	}
	go func() {
		t := time.NewTicker(deliverEvery)
		defer t.Stop()
		for {
			r.deliverAll(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-r.wake:
			}
		}
	}()
	return nil
}

func (r *remote) kick() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *remote) peers() []plugins.Peer {
	if r.h.deps.Peers == nil {
		return nil
	}
	list := r.h.deps.Peers()
	r.mu.Lock()
	for _, p := range list {
		r.seen[p.ID] = p
	}
	r.mu.Unlock()
	return list
}

// errNotPaired says what to do about it.
func errNotPaired(runner string) error {
	return fmt.Errorf("the runner %s is not paired with this hub: create a code in Settings, Plugins, Runners, and run lucode-runner pair <code> on it", runner)
}

// send seals and signs one message for one runner and waits for its answer, which only that
// runner, answering this very message, can have written.
func (r *remote) send(ctx context.Context, kind, runner string, payload any) (plugins.RunnerReply, error) {
	var reply plugins.RunnerReply
	nc := r.nc()
	if nc == nil {
		return reply, errors.New("this hub is not connected to any runner")
	}
	secret := dbpkg.PluginRunnerSecret(r.h.deps.DB, runner)
	if secret == nil {
		return plugins.RunnerReply{Code: plugins.RefuseNotPaired, Error: errNotPaired(runner).Error()}, nil
	}
	raw, nonce, err := plugins.SealRequest(secret, r.h.signer, kind, runner, payload)
	if err != nil {
		return reply, err
	}
	if len(raw) > plugins.MaxWireBytes {
		return reply, fmt.Errorf("this call is larger than %d KB, the most that reaches a runner at once", plugins.MaxWireBytes>>10)
	}
	msg, err := nc.RequestWithContext(ctx, plugins.RunnerSubject(runner), raw)
	if err != nil {
		if errors.Is(err, nats.ErrNoResponders) {
			return reply, fmt.Errorf("the runner %s is offline, or its build has no plugin support", runner)
		}
		return reply, err
	}
	reply, err = plugins.OpenReply(secret, runner, nonce, msg.Data)
	if err != nil {
		return reply, err
	}
	if reply.Code == plugins.RefuseNotPaired {
		reply.Error = errNotPaired(runner).Error()
	}
	return reply, nil
}

func (r *remote) note(pluginID, runner, sha, problem string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.delivered[pluginID] == nil {
		r.delivered[pluginID] = map[string]string{}
		r.failures[pluginID] = map[string]string{}
	}
	if problem == "" {
		r.delivered[pluginID][runner] = sha
		delete(r.failures[pluginID], runner)
	} else {
		r.failures[pluginID][runner] = problem
		delete(r.delivered[pluginID], runner)
	}
}

func (r *remote) has(pluginID, runner, sha string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.delivered[pluginID][runner] == sha
}

// deliverAll brings every online runner up to date. A runner remembers what it has, so this
// also finds out about runners that restarted.
func (r *remote) deliverAll(ctx context.Context) {
	peers := r.peers()
	if len(peers) == 0 {
		return
	}
	r.h.mu.RLock()
	list := make([]*loadedPlugin, 0, len(r.h.loaded))
	for _, l := range r.h.loaded {
		if l.row.Enabled && l.manifest.Targets.Runner != nil && l.pkg != nil {
			list = append(list, l)
		}
	}
	r.h.mu.RUnlock()
	for _, l := range list {
		for _, p := range peers {
			if p.Policy == plugins.PolicyOff {
				r.note(l.row.ID, p.ID, "", "this runner's policy refuses plugins")
				continue
			}
			if dbpkg.PluginRunnerSecret(r.h.deps.DB, p.ID) == nil {
				continue // nothing goes to a runner nobody paired
			}
			if r.has(l.row.ID, p.ID, l.row.SHA256) {
				continue
			}
			if err := r.deliver(ctx, l, p.ID); err != nil {
				log.Printf("[plugins] deliver %s to %s: %v", l.row.ID, p.ID, err)
			}
		}
	}
}

func (r *remote) deliver(ctx context.Context, l *loadedPlugin, runner string) error {
	info, err := os.Stat(l.row.Zip)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, deliverTimeout)
	defer cancel()
	reply, err := r.send(ctx, plugins.KindDeliver, runner, plugins.DeliverPayload{
		PluginID: l.row.ID, Version: l.row.Version, SHA256: l.row.SHA256, Size: info.Size(),
	})
	if err != nil {
		r.note(l.row.ID, runner, "", err.Error())
		return err
	}
	if !reply.OK {
		r.note(l.row.ID, runner, "", reply.Error)
		return errors.New(reply.Error)
	}
	r.note(l.row.ID, runner, l.row.SHA256, "")
	return nil
}

func (r *remote) unload(pluginID string) {
	r.mu.Lock()
	delete(r.delivered, pluginID)
	delete(r.failures, pluginID)
	r.mu.Unlock()
	if r.nc() == nil {
		return
	}
	for _, p := range r.peers() {
		go func(id string) {
			ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
			defer cancel()
			_, _ = r.send(ctx, plugins.KindUnload, id, plugins.UnloadPayload{PluginID: pluginID})
		}(p.ID)
	}
}

// servePackage hands out a sealed chunk of a package an enabled plugin has, to a paired runner. The
// runner checks the whole by its sha256, which came in a signed message.
func (r *remote) servePackage(m *nats.Msg) {
	empty, _ := json.Marshal(plugins.PackageChunk{})
	sha := m.Subject[len("lucode.plugin.package."):]
	var sealed plugins.Sealed
	if json.Unmarshal(m.Data, &sealed) != nil || sealed.Runner == "" {
		_ = m.Respond(empty)
		return
	}
	secret := dbpkg.PluginRunnerSecret(r.h.deps.DB, sealed.Runner)
	if secret == nil {
		_ = m.Respond(empty)
		return
	}
	req, err := plugins.OpenChunkRequest(secret, sealed.Runner, sha, sealed.Box)
	if err != nil || req.Offset < 0 {
		_ = m.Respond(empty)
		return
	}
	r.h.mu.RLock()
	file := ""
	for _, l := range r.h.loaded {
		if l.row.SHA256 == sha && l.row.Enabled {
			file = l.row.Zip
		}
	}
	r.h.mu.RUnlock()
	if file == "" {
		_ = m.Respond(empty)
		return
	}
	f, err := os.Open(file)
	if err != nil {
		_ = m.Respond(empty)
		return
	}
	defer f.Close()
	buf := make([]byte, plugins.ChunkBytes)
	n, err := f.ReadAt(buf, req.Offset)
	if err != nil && !errors.Is(err, io.EOF) {
		_ = m.Respond(empty)
		return
	}
	box, err := plugins.SealChunk(secret, sealed.Runner, sha, req.Offset, buf[:n])
	if err != nil {
		_ = m.Respond(empty)
		return
	}
	out, _ := json.Marshal(plugins.PackageChunk{Box: box})
	_ = m.Respond(out)
}

// invoke runs a plugin's code on a runner.
func (r *remote) invoke(ctx context.Context, l *loadedPlugin, runner string, path []string, arg any, timeout time.Duration, c plugins.Context, workspace string) (any, error) {
	if l.manifest.Targets.Runner == nil {
		return nil, fmt.Errorf("the plugin %s has no runner code", l.manifest.Name)
	}
	if runner == "" {
		return nil, errors.New("there is no runner for this: choose one in the project's settings")
	}
	if !l.row.Enabled {
		return nil, fmt.Errorf("the plugin %s is disabled", l.manifest.Name)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second) // the runner's own deadline goes first
	defer cancel()
	id := uuid.New().String()
	token := uuid.New().String()
	r.mu.Lock()
	r.tokens[token] = &callToken{pluginID: l.row.ID, runner: runner, ctx: ctx, context: c}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.tokens, token)
		r.mu.Unlock()
	}()
	payload := plugins.InvokePayload{
		ID: id, PluginID: l.row.ID, SHA256: l.row.SHA256, Granted: l.row.Granted, Path: path, Arg: arg,
		TimeoutMS: timeout.Milliseconds(), Context: c, Workspace: workspace,
		CallSubject: r.call, CallToken: token,
	}
	stop := context.AfterFunc(ctx, func() {
		cctx, ccancel := context.WithTimeout(context.Background(), statusTimeout)
		defer ccancel()
		_, _ = r.send(cctx, plugins.KindCancel, runner, plugins.CancelPayload{ID: id})
	})
	defer stop()

	for attempt := 0; ; attempt++ {
		reply, err := r.send(ctx, plugins.KindInvoke, runner, payload)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("the runner %s did not answer in time", runner)
			}
			return nil, err
		}
		for _, line := range reply.Logs {
			l.rt.Log(line.Level, "runner "+runner+": "+line.Text)
		}
		if reply.OK {
			return reply.Value, nil
		}
		// not there yet: this is what brings a plugin to a runner the loop has not reached
		if reply.Code == plugins.RefuseNotDelivered && attempt == 0 {
			if err := r.deliver(ctx, l, runner); err != nil {
				return nil, fmt.Errorf("the plugin could not be delivered to the runner %s: %w", runner, err)
			}
			continue
		}
		if reply.Code == plugins.RefuseNotDelivered {
			r.note(l.row.ID, runner, "", reply.Error)
		}
		if reply.Code == "no_handler" {
			return nil, plugins.ErrNoHandler
		}
		l.rt.Log("error", "runner "+runner+": "+reply.Error)
		return nil, errors.New(reply.Error)
	}
}

// serveCall is lucode.server.call. It must be sealed by a paired runner, fresh and not seen before,
// and carry the token of an invocation running on that runner. That reaches that invocation's own
// plugin and nothing else.
func (r *remote) serveCall(m *nats.Msg) {
	var sealed plugins.Sealed
	if json.Unmarshal(m.Data, &sealed) != nil || sealed.Runner == "" {
		return // no key to answer with, and nobody who should hear anything
	}
	secret := dbpkg.PluginRunnerSecret(r.h.deps.DB, sealed.Runner)
	if secret == nil {
		return
	}
	call, err := plugins.OpenCall(secret, sealed.Runner, sealed.Box)
	if err != nil {
		return
	}
	respond := func(rep plugins.RunnerReply) {
		raw, err := plugins.SealCallReply(secret, sealed.Runner, call.Nonce, rep)
		if err != nil || len(raw) > plugins.MaxWireBytes {
			raw, _ = plugins.SealCallReply(secret, sealed.Runner, call.Nonce, plugins.RunnerReply{Error: "this answer is too large to reach a runner"})
		}
		_ = m.Respond(raw)
	}
	if !r.freshCall(sealed.Runner, call) {
		respond(plugins.RunnerReply{Error: "this call is too old or was already used"})
		return
	}
	r.mu.Lock()
	tok, ok := r.tokens[call.Token]
	r.mu.Unlock()
	if !ok || tok.runner != sealed.Runner {
		respond(plugins.RunnerReply{Error: "this call does not belong to a running invocation"})
		return
	}
	l, err := r.h.usable(tok.pluginID)
	if err != nil {
		respond(plugins.RunnerReply{Error: err.Error()})
		return
	}
	v, err := r.h.invokeServer(tok.ctx, l, []string{"rpc", call.RPC}, call.Args, plugins.RPCSeconds*time.Second, tok.context, nil)
	if err != nil {
		respond(plugins.RunnerReply{Error: publicError(l, err).Error()})
		return
	}
	respond(plugins.RunnerReply{OK: true, Value: v})
}

// freshCall refuses a call seen before, or outside the window a nonce is remembered for.
func (r *remote) freshCall(runner string, call plugins.ServerCall) bool {
	now := time.Now()
	at := time.UnixMilli(call.TS)
	if call.Nonce == "" || at.Before(now.Add(-plugins.EnvelopeWindow)) || at.After(now.Add(plugins.EnvelopeWindow)) {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for n, t := range r.callsSeen {
		if now.Sub(t) > 2*plugins.EnvelopeWindow {
			delete(r.callsSeen, n)
		}
	}
	key := runner + " " + call.Nonce
	if _, dup := r.callsSeen[key]; dup {
		return false
	}
	r.callsSeen[key] = now
	return true
}

// PairRunner makes a new secret for a runner and the code that carries it there. Calling it again
// replaces the secret, which is also how a runner is revoked.
func (h *Host) PairRunner(runnerID string) (string, string, error) {
	if !runnerIDRe.MatchString(runnerID) {
		return "", "", errors.New("this is not a runner id")
	}
	secret, err := plugins.NewPairingSecret()
	if err != nil {
		return "", "", err
	}
	code, err := plugins.PairingCode(plugins.Pairing{Runner: runnerID, HubKey: h.signer.PublicKey(), Secret: secret})
	if err != nil {
		return "", "", err
	}
	if err := dbpkg.SavePluginRunnerSecret(h.deps.DB, runnerID, secret); err != nil {
		return "", "", err
	}
	h.remote.forget(runnerID)
	h.remote.kick()
	return code, "lucode-runner pair " + code, nil
}

// forget drops what was known about a runner: it has a new secret, so nothing it said counts.
func (r *remote) forget(runner string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.delivered {
		delete(m, runner)
	}
	for _, m := range r.failures {
		delete(m, runner)
	}
}

// Runners shows a plugin's state on each runner.
func (h *Host) Runners(id string) ([]plugins.RunnerRow, error) {
	l, err := h.get(id)
	if err != nil {
		return nil, err
	}
	out := []plugins.RunnerRow{}
	if l.manifest.Targets.Runner == nil {
		return out, nil
	}
	r := h.remote
	online := map[string]bool{}
	for _, p := range r.peers() {
		online[p.ID] = true
		row := plugins.RunnerRow{RunnerID: p.ID, Hostname: p.Hostname, Online: true, Policy: p.Policy, Version: p.Version}
		ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
		reply, err := r.send(ctx, plugins.KindStatus, p.ID, plugins.UnloadPayload{PluginID: id})
		cancel()
		switch {
		case err != nil:
			row.Error = err.Error()
		case !reply.OK:
			row.Error = reply.Error
		default:
			row.Paired = true
			var st plugins.RunnerStatus
			raw, _ := json.Marshal(reply.Value)
			if json.Unmarshal(raw, &st) == nil {
				row.Delivered, row.Version = st.Delivered, st.Version
				if st.Policy != "" {
					row.Policy = st.Policy
				}
			}
		}
		if row.Error == "" {
			r.mu.Lock()
			row.Error = r.failures[id][p.ID]
			r.mu.Unlock()
		}
		out = append(out, row)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rid := range plugins.SortedKeys(r.seen) {
		if !online[rid] {
			p := r.seen[rid]
			paired := dbpkg.PluginRunnerSecret(r.h.deps.DB, rid) != nil
			out = append(out, plugins.RunnerRow{RunnerID: rid, Hostname: p.Hostname, Online: false, Policy: p.Policy, Paired: paired, Delivered: r.delivered[id][rid] != "", Version: p.Version})
		}
	}
	return out, nil
}
