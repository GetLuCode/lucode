package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/plugins"
)

const maxNodeOutputBytes = 256 << 10

// invokeServer runs an export of the server target with its effective permissions.
func (h *Host) invokeServer(ctx context.Context, l *loadedPlugin, path []string, arg any, timeout time.Duration, c plugins.Context, progress func(string)) (any, error) {
	if l.manifest.Targets.Server == nil {
		return nil, fmt.Errorf("the plugin %s has no server code", l.manifest.Name)
	}
	perms := plugins.Effective(plugins.TargetServer, l.manifest.Targets.Server.Permissions, l.row.Granted)
	return l.rt.Invoke(ctx, plugins.Invocation{
		Path: path, Arg: arg, Timeout: timeout, Context: c, Perms: perms,
		Modules: h.serverModules(l, perms), Progress: progress,
	})
}

// rpcAllowed is the allowlist of the browser's road in: what a contribution names, or what a
// view tree of this plugin offered as an action.
func rpcAllowed(l *loadedPlugin, name string) bool {
	c := l.manifest.Contributes
	for _, p := range c.Pages {
		if p.Render == name {
			return true
		}
	}
	for _, list := range [][]plugins.ActionDef{c.ChatActions, c.CardActions} {
		for _, a := range list {
			if a.RPC == name {
				return true
			}
		}
	}
	l.allowMu.Lock()
	defer l.allowMu.Unlock()
	return l.allowed[name]
}

// rememberActions records the rpcs a view tree offers, so the browser can call them.
func rememberActions(l *loadedPlugin, v any) {
	found := []string{}
	var walk func(any, int)
	walk = func(v any, depth int) {
		if depth > 64 {
			return
		}
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				walk(e, depth+1)
			}
		case map[string]any:
			for k, e := range x {
				if k == "action" || k == "submit" {
					if m, ok := e.(map[string]any); ok {
						if name, ok := m["rpc"].(string); ok {
							found = append(found, name)
						}
					}
				}
				walk(e, depth+1)
			}
		}
	}
	walk(v, 0)
	l.allowMu.Lock()
	defer l.allowMu.Unlock()
	for _, name := range found {
		if len(l.allowed) < 2000 {
			l.allowed[name] = true
		}
	}
}

func runnerAction(l *loadedPlugin, name string) bool {
	c := l.manifest.Contributes
	for _, list := range [][]plugins.ActionDef{c.ChatActions, c.CardActions} {
		for _, a := range list {
			if a.RPC == name && a.Target == plugins.TargetRunner {
				return true
			}
		}
	}
	return false
}

// RPC calls a plugin from the browser.
func (h *Host) RPC(ctx context.Context, id, name, projectID string, args any) (any, error) {
	l, err := h.usable(id)
	if err != nil {
		return nil, err
	}
	if !rpcAllowed(l, name) {
		return nil, fmt.Errorf("%w: the plugin %s has no call %q for the browser", plugins.ErrRPCRefused, l.manifest.Name, name)
	}
	merged := map[string]any{}
	if m, ok := args.(map[string]any); ok {
		for k, v := range m {
			merged[k] = v
		}
	} else if args != nil {
		merged["value"] = args
	}
	merged["projectId"] = projectID
	c := plugins.Context{ProjectID: projectID, Kind: "rpc"}
	c.SessionID, _ = merged["sessionId"].(string)
	c.CardID, _ = merged["cardId"].(string)

	var res any
	if runnerAction(l, name) {
		runner, dir, werr := h.deps.Services.Workspace(projectID, c.SessionID, c.CardID, "")
		if werr != nil {
			return nil, werr
		}
		res, err = h.remote.invoke(ctx, l, runner, []string{"rpc", name}, merged, plugins.RPCSeconds*time.Second, c, dir)
	} else {
		res, err = h.invokeServer(ctx, l, []string{"rpc", name}, merged, plugins.RPCSeconds*time.Second, c, nil)
	}
	if err != nil {
		return nil, publicError(l, err)
	}
	rememberActions(l, res)
	return l.scrubValue(res), nil
}

func publicError(l *loadedPlugin, err error) error {
	if errors.Is(err, plugins.ErrNoHandler) {
		return errors.New(l.scrubText(fmt.Sprintf("the plugin %s has no handler for this", l.manifest.Name)))
	}
	return errors.New(l.scrubText(plugins.Scrub(err.Error())))
}

// CheckModel says whether a chat can use this model id right now. The id comes from the browser
// or a saved session, so it is checked against what enabled plugins declare.
func (h *Host) CheckModel(wire string) error {
	_, _, err := h.resolveModel(wire)
	return err
}

func (h *Host) resolveModel(wire string) (*loadedPlugin, plugins.ModelDef, error) {
	pid, mid, ok := plugins.ParseWireModel(wire)
	if !ok {
		return nil, plugins.ModelDef{}, fmt.Errorf("%q is not a plugin model", wire)
	}
	l, err := h.usable(pid)
	if err != nil {
		return nil, plugins.ModelDef{}, err
	}
	def, ok := l.manifest.Model(mid)
	if !ok {
		return nil, def, fmt.Errorf("the plugin %s has no model %q", l.manifest.Name, mid)
	}
	return l, def, nil
}

// Turn answers a chat message.
func (h *Host) Turn(ctx context.Context, req plugins.TurnRequest, progress func(string)) (string, error) {
	l, def, err := h.resolveModel(req.Model)
	if err != nil {
		return "", err
	}
	secs := def.TimeoutSeconds
	if secs <= 0 {
		secs = plugins.DefaultTurnSeconds
	}
	var image any
	if req.ImageBase64 != "" {
		image = map[string]any{"base64": req.ImageBase64, "mediaType": req.ImageMediaType}
	}
	arg := map[string]any{
		"sessionId": req.SessionID, "projectId": req.ProjectID, "message": req.Message, "image": image, "model": def.ID,
	}
	c := plugins.Context{ProjectID: req.ProjectID, SessionID: req.SessionID, Kind: "turn"}
	v, err := h.invokeServer(ctx, l, []string{"models", def.ID, "turn"}, arg, time.Duration(secs)*time.Second, c, progress)
	if err != nil {
		return "", publicError(l, err)
	}
	text, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("the plugin %s must answer a turn with text", l.manifest.Name)
	}
	return text, nil
}

// FlowNodes lists every installed plugin's nodes. A disabled plugin's stay, flagged: the editor
// must never lose a node.
func (h *Host) FlowNodes() []plugins.NodeInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := []plugins.NodeInfo{}
	for _, id := range plugins.SortedKeys(h.loaded) {
		l := h.loaded[id]
		for _, d := range l.manifest.Contributes.FlowNodes {
			out = append(out, plugins.NodeInfo{
				Type: NodeType(id, d.ID), PluginID: id, PluginName: l.manifest.Name, Disabled: !l.row.Enabled, Def: d,
			})
		}
	}
	return out
}

// NodeType is a plugin node's type in a graph.
func NodeType(pluginID, nodeID string) string { return "plugin." + pluginID + "." + nodeID }

// parseNodeType splits plugin.<plugin id>.<node id>: a node id has no dots.
func parseNodeType(t string) (pluginID, nodeID string, ok bool) {
	rest, found := strings.CutPrefix(t, "plugin.")
	if !found {
		return "", "", false
	}
	i := strings.LastIndex(rest, ".")
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	return rest[:i], rest[i+1:], true
}

// Blueprints lists the blueprints of what is installed.
func (h *Host) Blueprints() []plugins.BlueprintInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := []plugins.BlueprintInfo{}
	for _, id := range plugins.SortedKeys(h.loaded) {
		l := h.loaded[id]
		if l.pkg == nil {
			continue
		}
		for _, b := range l.manifest.Contributes.Blueprints {
			raw, err := l.pkg.ReadFile(b.File, plugins.MaxFileBytes)
			if err != nil {
				continue
			}
			out = append(out, plugins.BlueprintInfo{PluginID: id, PluginName: l.manifest.Name, Def: b, Graph: raw})
		}
	}
	return out
}

// RunNode runs a plugin's flow node, on the hub or on the card's runner. It blocks for as long
// as the node's timeout allows; the engine calls it away from its own goroutine.
func (h *Host) RunNode(ctx context.Context, call plugins.NodeCall) (string, map[string]any, error) {
	pid, nid, ok := parseNodeType(call.Type)
	if !ok {
		return "", nil, fmt.Errorf("%q is not a plugin node", call.Type)
	}
	l, err := h.usable(pid)
	if err != nil {
		return "", nil, err
	}
	def, ok := l.manifest.Node(nid)
	if !ok {
		return "", nil, fmt.Errorf("the plugin %s no longer has the node %q", l.manifest.Name, nid)
	}
	secs := def.TimeoutSeconds
	if secs <= 0 {
		secs = plugins.DefaultNodeSeconds
	}
	timeout := time.Duration(secs) * time.Second
	arg := map[string]any{
		"config": call.Config, "card": call.Card, "event": call.Event, "nodes": call.Nodes, "run": call.Run, "projectId": call.ProjectID,
	}
	c := plugins.Context{ProjectID: call.ProjectID, CardID: call.CardID, Kind: "flow", FlowID: call.FlowID, RunID: call.RunID, Depth: call.Depth}
	var res any
	if def.Target == plugins.TargetRunner {
		runner, dir, werr := h.deps.Services.Workspace(call.ProjectID, "", call.CardID, "")
		if werr != nil {
			return "", nil, werr
		}
		res, err = h.remote.invoke(ctx, l, runner, []string{"nodes", nid}, arg, timeout, c, dir)
	} else {
		res, err = h.invokeServer(ctx, l, []string{"nodes", nid}, arg, timeout, c, nil)
	}
	if err != nil {
		return "", nil, publicError(l, err)
	}
	return nodeResult(l, def, res)
}

// nodeResult reads what a node returned. Only a declared port can choose a path.
func nodeResult(l *loadedPlugin, def plugins.FlowNodeDef, res any) (string, map[string]any, error) {
	port, output := "", map[string]any{}
	if m, ok := res.(map[string]any); ok {
		port, _ = m["port"].(string)
		if o, ok := m["output"].(map[string]any); ok {
			output = o
		} else if m["output"] != nil {
			output = map[string]any{"value": m["output"]}
		}
	} else if res != nil {
		output = map[string]any{"value": res}
	}
	if port == "" {
		for _, p := range def.Ports {
			if p != "failed" {
				port = p
				break
			}
		}
	}
	found := false
	for _, p := range def.Ports {
		found = found || p == port
	}
	if !found {
		return "", nil, fmt.Errorf("the plugin %s returned the port %q, which its node %q does not have", l.manifest.Name, port, def.ID)
	}
	output, _ = l.scrubValue(output).(map[string]any)
	if raw, err := json.Marshal(output); err != nil || len(raw) > maxNodeOutputBytes {
		return "", nil, fmt.Errorf("the plugin %s returned more than %d KB from its node %q", l.manifest.Name, maxNodeOutputBytes>>10, def.ID)
	}
	return port, output, nil
}

// ---- events ----

// fireEvent runs an event handler in the background, if the plugin has one. Each one runs with
// its own short deadline.
func (h *Host) fireEvent(l *loadedPlugin, name, projectID string, payload map[string]any, depth int) {
	if l.rt == nil || !l.row.Enabled || l.manifest.Targets.Server == nil {
		return
	}
	if _, skip := l.noEvent.Load(name); skip {
		return
	}
	go h.fireEventWait(l, name, projectID, payload, depth)
}

func (h *Host) fireEventWait(l *loadedPlugin, name, projectID string, payload map[string]any, depth int) {
	if l.manifest.Targets.Server == nil {
		return
	}
	c := plugins.Context{ProjectID: projectID, Kind: "event", Depth: depth}
	if s, ok := payload["sessionId"].(string); ok {
		c.SessionID = s
	}
	if s, ok := payload["cardId"].(string); ok {
		c.CardID = s
	}
	_, err := h.invokeServer(h.ctx, l, []string{"events", name}, payload, plugins.EventSeconds*time.Second, c, nil)
	if errors.Is(err, plugins.ErrNoHandler) {
		l.noEvent.Store(name, true)
		return
	}
	if err != nil {
		log.Printf("[plugins] %s event %s: %v", l.row.ID, name, publicError(l, err))
	}
}

// DeliverEvent tells the enabled plugins something happened. Past the flows' depth limit it stops,
// so a plugin that reacts to its own moves cannot run away.
func (h *Host) DeliverEvent(name, projectID string, payload map[string]any, depth int) {
	if depth >= h.deps.MaxDepth {
		return
	}
	h.mu.RLock()
	list := make([]*loadedPlugin, 0, len(h.loaded))
	for _, l := range h.loaded {
		list = append(list, l)
	}
	h.mu.RUnlock()
	for _, l := range list {
		// what happens on the board is for plugins that may read it
		if name == "cardMoved" && !canReadBoard(l) {
			continue
		}
		h.fireEvent(l, name, projectID, payload, depth)
	}
}

func canReadBoard(l *loadedPlugin) bool {
	if l.manifest.Targets.Server == nil {
		return false
	}
	return plugins.Effective(plugins.TargetServer, l.manifest.Targets.Server.Permissions, l.row.Granted).Board != ""
}

// SessionClosed tells the plugins that have a conversation in a session that it is over. Everything
// happens away from the caller, so neither the lookup nor a handler can hold up or fail a request.
func (h *Host) SessionClosed(projectID, sessionID string) {
	go func() {
		h.mu.RLock()
		list := make([]*loadedPlugin, 0, len(h.loaded))
		for _, l := range h.loaded {
			list = append(list, l)
		}
		h.mu.RUnlock()
		for _, l := range list {
			if _, err := dbpkg.GetPluginConversationBySession(h.deps.DB, l.row.ID, sessionID); err != nil {
				continue
			}
			h.fireEvent(l, "sessionClosed", projectID, map[string]any{"sessionId": sessionID, "projectId": projectID}, 0)
		}
	}()
}

// SessionDeleted tells the plugins a chat is gone.
func (h *Host) SessionDeleted(projectID, sessionID string) {
	h.DeliverEvent("sessionDeleted", projectID, map[string]any{"sessionId": sessionID, "projectId": projectID}, 0)
}
