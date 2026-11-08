package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strings"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/flows"
	natspkg "claude-orchestrator/internal/natsbus"
	"claude-orchestrator/internal/plugins"
	sessionpkg "claude-orchestrator/internal/session"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
)

// Plugins: the routes of the host, which is in internal/plugins/hub. This file only knows the
// interface in internal/plugins, because lucode-runner links this package and must not link the
// host: cmd/lucode passes the factory in.

const pluginExecutor = "plugin"

// StartPlugins starts the host. Only the hub calls it, before StartFlows, so the engine finds
// the plugins' nodes from its first tick.
func (h *Handler) StartPlugins(ctx context.Context, dataDir string, nc *nats.Conn, factory plugins.HubFactory) error {
	// only this one installs by itself; the rest of the catalog waits for an admin
	return h.startPluginHost(ctx, plugins.HubDeps{DataDir: dataDir, NATS: nc, Relay: h.relay, AutoInstall: []string{plugins.GeminiID}}, factory)
}

func (h *Handler) startPluginHost(ctx context.Context, deps plugins.HubDeps, factory plugins.HubFactory) error {
	deps.DB = h.db
	deps.Services = pluginServices{h}
	deps.MaxDepth = flows.MaxDepth
	if deps.Peers == nil {
		deps.Peers = h.pluginPeers
	}
	host, err := factory(deps)
	if err != nil {
		return err
	}
	h.plugins = host
	flows.SetPluginSource(pluginFlowSource{h})
	if h.manager != nil {
		h.manager.PluginTurn = func(ctx context.Context, req sessionpkg.PluginTurnRequest, progress func(string)) (string, error) {
			return host.Turn(ctx, plugins.TurnRequest(req), progress)
		}
	}
	return host.Start(ctx)
}

// pluginPeers are the runners on the bus, with the policy each announces.
func (h *Handler) pluginPeers() []plugins.Peer {
	if h.bus == nil {
		return nil
	}
	out := []plugins.Peer{}
	for _, p := range h.bus.Peers() {
		if p.Type != "lucode" || !p.IsRunner() {
			continue
		}
		policy, _ := p.Extra[plugins.DiscoveryPolicyKey].(string)
		version, _ := p.Extra["version"].(string)
		out = append(out, plugins.Peer{ID: p.ID, Hostname: p.Hostname, Policy: policy, Version: version})
	}
	return out
}

// pluginEventTap passes on what plugins can react to. The host runs each handler on its own.
func (h *Handler) pluginEventTap(ev dbpkg.FlowEvent) {
	if h.plugins == nil || ev.Type != dbpkg.EventCardMoved {
		return
	}
	payload := map[string]any{"cardId": ev.CardID, "projectId": ev.ProjectID}
	for k, v := range ev.Payload {
		payload[k] = v
	}
	h.plugins.DeliverEvent("cardMoved", ev.ProjectID, payload, ev.Depth)
}

// ---- who is asking ----

type authInfoKey struct{}

// authInfo is what authMiddleware found out about a request.
type authInfo struct {
	Bearer  bool
	IsAdmin bool
}

func withAuthInfo(r *http.Request, info authInfo) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authInfoKey{}, info))
}

// requireAdmin lets only an admin's bearer token through. A request over NATS passed auth without
// any proof of who sent it, so it is never enough here; neither is an API key.
func (h *Handler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if natspkg.IsRPC(r.Context()) {
		http.Error(w, "plugins cannot be managed from another instance", http.StatusForbidden)
		return false
	}
	info, _ := r.Context().Value(authInfoKey{}).(authInfo)
	if !info.Bearer || !info.IsAdmin {
		http.Error(w, "this needs an administrator signed in", http.StatusForbidden)
		return false
	}
	return true
}

// refuseLockedFlowRPC: a plugin's flow runs plugin code, and a request over NATS has no proof of who
// sent it, so it cannot switch one on, run it or copy it.
func refuseLockedFlowRPC(w http.ResponseWriter, r *http.Request, f dbpkg.Flow) bool {
	if !f.Locked || !natspkg.IsRPC(r.Context()) {
		return false
	}
	http.Error(w, "a plugin's flow cannot be changed or run from another instance", http.StatusForbidden)
	return true
}

// refusePluginTurnRPC is the same for a turn on a plugin's model. Ordinary turns over NATS are how
// instances work, and stay as they are.
func refusePluginTurnRPC(w http.ResponseWriter, r *http.Request, model string) bool {
	if !natspkg.IsRPC(r.Context()) || (model != plugins.LegacyAIMode && !plugins.IsPluginModel(model)) {
		return false
	}
	http.Error(w, "a plugin's model cannot be used from another instance", http.StatusForbidden)
	return true
}

// ---- turns ----

// pluginModelOf says whether a model id goes to a plugin. The old id of Google AI Mode, which old
// sessions and saved choices have, means that plugin's model; without the plugin it fails like any
// other plugin's model.
func (h *Handler) pluginModelOf(model string) (wire string, isPlugin bool, err error) {
	if model == plugins.LegacyAIMode {
		model = plugins.MappedAIMode
	}
	if !plugins.IsPluginModel(model) {
		return "", false, nil
	}
	if h.plugins == nil {
		return "", true, errors.New("plugins are not running on this instance")
	}
	if err := h.plugins.CheckModel(model); err != nil {
		return "", true, err
	}
	return model, true, nil
}

// applyPluginModel makes a turn go to a plugin if its model is one.
func (h *Handler) applyPluginModel(opts *sessionpkg.TurnOptions) error {
	wire, isPlugin, err := h.pluginModelOf(opts.Model)
	if !isPlugin || err != nil {
		return err
	}
	opts.Model, opts.Executor = wire, pluginExecutor
	opts.Persistent, opts.Resume, opts.AutoMode, opts.Worktree = false, false, false, false
	opts.RunnerID, opts.Tools, opts.LucodeTools = "", nil, nil
	return nil
}

// pluginTurnOpts builds a turn on a plugin's model. A plugin needs no runner, AI or tools.
func (h *Handler) pluginTurnOpts(sess dbpkg.Session, message, model string) (sessionpkg.TurnOptions, bool, error) {
	wire, isPlugin, err := h.pluginModelOf(model)
	if !isPlugin {
		return sessionpkg.TurnOptions{}, false, nil
	}
	if err != nil {
		return sessionpkg.TurnOptions{}, true, err
	}
	return sessionpkg.TurnOptions{
		SessionID: sess.ID, ProjectID: sess.ProjectID, Message: message, Model: wire,
		Executor: pluginExecutor, AgentName: "orchestrator",
	}, true, nil
}

// ---- services for the host ----

type pluginServices struct{ h *Handler }

func causeOf(c plugins.Cause) dbpkg.EventCause {
	return dbpkg.EventCause{Source: c.Source, FlowID: c.FlowID, RunID: c.RunID, Depth: c.Depth}
}

func (s pluginServices) CreateCard(projectID, title, description, status, agent string, cause plugins.Cause) (string, error) {
	return flowHost{s.h}.CreateTask(projectID, title, description, status, agent, causeOf(cause))
}

func (s pluginServices) MoveCard(cardID, kind, listID string, cause plugins.Cause) (string, error) {
	rejected, err := flowHost{s.h}.MoveCard(cardID, kind, listID, causeOf(cause))
	s.h.notifyFlows()
	return rejected, err
}

func (s pluginServices) CommentCard(cardID, author, text string) error {
	card, err := dbpkg.GetKanbanCard(s.h.db, cardID)
	if err != nil {
		return err
	}
	if err := dbpkg.InsertCardComment(s.h.db, dbpkg.CardComment{
		ID: uuid.New().String(), CardID: cardID, Author: author, Kind: "comment", Body: text,
	}); err != nil {
		return err
	}
	s.h.notifyTaskUpdated("", card)
	return nil
}

func (s pluginServices) AddCardTag(cardID, tag string) error {
	return flowHost{s.h}.AddTag(cardID, tag)
}

func (s pluginServices) Broadcast(sessionID string, ev map[string]any) {
	if s.h.bus != nil {
		s.h.bus.Broadcast(sessionID, ev)
	}
}

func (s pluginServices) NotifyFlows() { s.h.notifyFlows() }

func (s pluginServices) ValidateGraph(raw json.RawMessage) []string {
	g, err := flows.ParseGraph(raw)
	if err != nil {
		return []string{err.Error()}
	}
	out := []string{}
	for _, p := range flows.Validate(g) {
		out = append(out, p.Message)
	}
	return out
}

// Workspace is decided here, from what the session or card is, and never by a plugin. The card's
// worktree comes first, then where its session last worked, then the project's folder on that runner.
func (s pluginServices) Workspace(projectID, sessionID, cardID, runnerID string) (string, string, error) {
	h := s.h
	proj, err := dbpkg.GetProject(h.db, projectID)
	if err != nil {
		return "", "", errors.New("this call has no project")
	}
	agentID := ""
	if sessionID != "" {
		sess, err := dbpkg.GetSession(h.db, sessionID)
		if err != nil || sess.ProjectID != projectID {
			return "", "", fmt.Errorf("no session %q in this project", sessionID)
		}
		agentID = sess.AgentID
		if cardID == "" {
			cardID = sess.CardID
		}
	}
	var card dbpkg.KanbanCard
	if cardID != "" {
		if card, err = dbpkg.GetKanbanCard(h.db, cardID); err != nil || card.ProjectID != projectID {
			return "", "", fmt.Errorf("no card %q in this project", cardID)
		}
		if agentID == "" {
			agentID = card.AgentID
		}
	}
	runner := runnerID
	if runner == "" && agentID != "" {
		if a, err := dbpkg.GetAgent(h.db, agentID); err == nil {
			runner = a.RunnerID
		}
	}
	if runner == "" {
		runner = proj.RunnerID
	}
	if runner == "" {
		return "", "", fmt.Errorf("project %q has no runner: choose one in its settings", proj.Name)
	}
	if cardID != "" {
		if rv, err := dbpkg.GetCardReview(h.db, cardID); err == nil && (rv.RunnerID == "" || rv.RunnerID == runner) {
			if rv.WorktreePath != "" {
				return runner, rv.WorktreePath, nil
			}
			if rv.RepoPath != "" {
				return runner, rv.RepoPath, nil
			}
		}
		if sessionID == "" {
			if sess, err := dbpkg.GetSessionByCard(h.db, cardID); err == nil {
				sessionID = sess.ID
			}
		}
	}
	if sessionID != "" {
		if ws := dbpkg.GetSessionWorkspace(h.db, sessionID); ws != nil && runner == proj.RunnerID {
			if cwd, ok := ws["cwd"].(string); ok && cwd != "" {
				return runner, cwd, nil
			}
		}
	}
	if dirs := proj.EnvDirs[runner]; len(dirs) > 0 {
		return runner, dirs[0], nil
	}
	return runner, "", nil
}

// ---- flows ----

type pluginFlowSource struct{ h *Handler }

func (s pluginFlowSource) NodeTypes() []flows.NodeType {
	if s.h.plugins == nil {
		return nil
	}
	out := []flows.NodeType{}
	for _, n := range s.h.plugins.FlowNodes() {
		fields := make([]flows.Field, 0, len(n.Def.Fields))
		for _, f := range n.Def.Fields {
			fields = append(fields, flows.Field{
				Name: f.Name, Label: f.Label, Kind: f.Kind, Required: f.Required, Options: f.Options,
				Default: f.Default, Help: f.Help, Min: f.Min, PortLabel: f.PortLabel,
			})
		}
		secs := n.Def.TimeoutSeconds
		if secs <= 0 {
			secs = plugins.DefaultNodeSeconds
		}
		out = append(out, flows.NodeType{
			Type: n.Type, Title: n.Def.Label, Category: flows.CategoryAction, Description: n.Def.Description,
			Ports: n.Def.Ports, Fields: fields, PluginID: n.PluginID, PluginName: n.PluginName, Icon: n.Def.Icon,
			Disabled: n.Disabled, TimeoutSeconds: secs, Target: n.Def.Target,
		})
	}
	return out
}

func (s pluginFlowSource) Blueprints() []flows.Blueprint {
	if s.h.plugins == nil {
		return nil
	}
	out := []flows.Blueprint{}
	for _, b := range s.h.plugins.Blueprints() {
		g, err := flows.ParseGraph(b.Graph)
		if err != nil {
			continue
		}
		out = append(out, flows.Blueprint{
			Key: b.PluginID + "/" + b.Def.Key, Name: b.Def.Name, Description: b.Def.Description, Graph: g,
			PluginID: b.PluginID, PluginName: b.PluginName,
		})
	}
	return out
}

func (s pluginFlowSource) Installed(id string) bool {
	if s.h.plugins == nil {
		return false
	}
	_, err := s.h.plugins.Get(id)
	return err == nil
}

func (s pluginFlowSource) RunNode(ctx context.Context, c flows.PluginCall) (string, map[string]any, error) {
	if s.h.plugins == nil {
		return "", nil, flows.ErrNoPluginHost()
	}
	return s.h.plugins.RunNode(ctx, plugins.NodeCall(c))
}

// flowSaveStatus says what a failed save of a draft means.
func flowSaveStatus(err error) int {
	if errors.Is(err, dbpkg.ErrFlowLocked) {
		return http.StatusConflict
	}
	return http.StatusInternalServerError
}

// forkFlow copies a plugin's locked flow as an ordinary draft.
func (h *Handler) forkFlow(w http.ResponseWriter, r *http.Request) {
	f, ok := h.flowFor(w, r)
	if !ok {
		return
	}
	if refuseLockedFlowRPC(w, r, f) {
		return
	}
	if !f.Locked {
		http.Error(w, "only a flow that belongs to a plugin is copied this way", http.StatusBadRequest)
		return
	}
	copied, err := dbpkg.ForkFlow(h.db, f.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusCreated, copied)
}

// ---- API ----

func (h *Handler) registerPlugins(mux *http.ServeMux) {
	need := func(f http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if h.plugins == nil {
				http.Error(w, "plugins are not running on this instance", http.StatusServiceUnavailable)
				return
			}
			f(w, r)
		}
	}
	admin := func(f http.HandlerFunc) http.HandlerFunc {
		return need(func(w http.ResponseWriter, r *http.Request) {
			if h.requireAdmin(w, r) {
				f(w, r)
			}
		})
	}
	mux.HandleFunc("GET /api/plugins", need(h.listPlugins))
	mux.HandleFunc("GET /api/plugins/ui", need(h.pluginsUI))
	mux.HandleFunc("GET /api/plugins/catalog", need(h.pluginCatalog))
	// one pattern for /catalog/{id}/install and /{id}/rpc/{name}: the mux refuses them as two, and
	// "catalog" cannot be a plugin's id, because an id has a dot
	install, rpc := admin(h.installBundledPlugin), need(h.pluginRPC)
	mux.HandleFunc("POST /api/plugins/{id}/{kind}/{name}", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.PathValue("id") == "catalog" && r.PathValue("name") == "install":
			r.SetPathValue("id", r.PathValue("kind"))
			install(w, r)
		case r.PathValue("kind") == "rpc":
			rpc(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	h.registerPluginUI(mux, need)
	mux.HandleFunc("POST /api/plugins/inspect", admin(h.inspectPlugin))
	mux.HandleFunc("POST /api/plugins/install", admin(h.installPlugin))
	mux.HandleFunc("PUT /api/plugins/{id}/enabled", admin(h.setPluginEnabled))
	mux.HandleFunc("DELETE /api/plugins/{id}", admin(h.deletePlugin))
	mux.HandleFunc("GET /api/plugins/{id}/settings", need(h.getPluginSettings))
	mux.HandleFunc("PUT /api/plugins/{id}/settings", admin(h.putPluginSettings))
	mux.HandleFunc("GET /api/plugins/{id}/logs", admin(h.pluginLogs))
	mux.HandleFunc("GET /api/plugins/{id}/assets/{path...}", need(h.pluginAsset))
	mux.HandleFunc("GET /api/plugins/{id}/runners", need(h.pluginRunners))
	mux.HandleFunc("POST /api/plugins/runners/{runnerId}/pair", admin(h.pairPluginRunner))
	mux.HandleFunc("GET /api/plugins/{id}/conversations", need(h.listPluginConversations))
	mux.HandleFunc("POST /api/plugins/{id}/conversations", need(h.newPluginConversation))
	mux.HandleFunc("POST /api/plugins/{id}/conversations/{cid}/continue", need(h.continuePluginConversation))
}

func pluginError(w http.ResponseWriter, err error) {
	if errors.Is(err, plugins.ErrNotFound) {
		http.Error(w, "plugin not found", http.StatusNotFound)
		return
	}
	http.Error(w, plugins.Scrub(err.Error()), http.StatusBadRequest)
}

func (h *Handler) listPlugins(w http.ResponseWriter, _ *http.Request) {
	jsonResp(w, http.StatusOK, h.plugins.List())
}

func (h *Handler) pluginsUI(w http.ResponseWriter, _ *http.Request) {
	jsonResp(w, http.StatusOK, h.plugins.UI())
}

func (h *Handler) pluginCatalog(w http.ResponseWriter, _ *http.Request) {
	jsonResp(w, http.StatusOK, h.plugins.Catalog())
}

func (h *Handler) installBundledPlugin(w http.ResponseWriter, r *http.Request) {
	v, err := h.plugins.InstallBundled(r.PathValue("id"))
	if err != nil {
		pluginError(w, err)
		return
	}
	jsonResp(w, http.StatusOK, v)
}

// inspectPlugin validates an upload and stages it. Nothing is installed until the admin grants it.
func (h *Handler) inspectPlugin(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, plugins.MaxZipBytes+1<<20)
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "send the plugin as the multipart field \"file\", up to 50 MB", http.StatusBadRequest)
		return
	}
	defer file.Close()
	tmp, err := os.CreateTemp("", "lucode-plugin-*.zip")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, file)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		http.Error(w, "the upload could not be read", http.StatusBadRequest)
		return
	}
	res, err := h.plugins.Inspect(tmp.Name())
	if err != nil {
		pluginError(w, err)
		return
	}
	jsonResp(w, http.StatusOK, res)
}

func (h *Handler) installPlugin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token   string   `json:"token"`
		Granted []string `json:"granted"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	v, err := h.plugins.Install(body.Token, body.Granted)
	if err != nil {
		pluginError(w, err)
		return
	}
	jsonResp(w, http.StatusOK, v)
}

func (h *Handler) setPluginEnabled(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	v, err := h.plugins.SetEnabled(r.PathValue("id"), body.Enabled)
	if err != nil {
		pluginError(w, err)
		return
	}
	jsonResp(w, http.StatusOK, v)
}

func (h *Handler) deletePlugin(w http.ResponseWriter, r *http.Request) {
	if err := h.plugins.Uninstall(r.PathValue("id"), r.URL.Query().Get("keepData") == "1"); err != nil {
		pluginError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getPluginSettings(w http.ResponseWriter, r *http.Request) {
	v, err := h.plugins.Settings(r.PathValue("id"))
	if err != nil {
		pluginError(w, err)
		return
	}
	jsonResp(w, http.StatusOK, v)
}

func (h *Handler) putPluginSettings(w http.ResponseWriter, r *http.Request) {
	values := map[string]any{}
	if err := json.NewDecoder(r.Body).Decode(&values); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := h.plugins.PutSettings(r.PathValue("id"), values); err != nil {
		pluginError(w, err)
		return
	}
	h.getPluginSettings(w, r)
}

func (h *Handler) pluginLogs(w http.ResponseWriter, r *http.Request) {
	v, err := h.plugins.Logs(r.PathValue("id"))
	if err != nil {
		pluginError(w, err)
		return
	}
	jsonResp(w, http.StatusOK, v)
}

// pluginAsset serves an image of a plugin so that it can never act as a page of this origin.
func (h *Handler) pluginAsset(w http.ResponseWriter, r *http.Request) {
	raw, ctype, err := h.plugins.Asset(r.PathValue("id"), r.PathValue("path"))
	if err != nil {
		pluginError(w, err)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:")
	w.Header().Set("Content-Disposition", `inline; filename="`+path.Base(strings.ReplaceAll(r.PathValue("path"), `"`, ""))+`"`)
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	_, _ = w.Write(raw)
}

// pluginRPC is the browser's call into a plugin. It runs plugin code, so like the admin routes it
// needs a person: a request over NATS comes with no proof of who sent it.
func (h *Handler) pluginRPC(w http.ResponseWriter, r *http.Request) {
	if natspkg.IsRPC(r.Context()) {
		http.Error(w, "plugins cannot be called from another instance", http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, plugins.MaxCallBytes)
	var body struct {
		ProjectID string `json:"projectId"`
		Args      any    `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.ProjectID != "" {
		if _, err := dbpkg.GetProject(h.db, body.ProjectID); err != nil {
			http.Error(w, "project not found", http.StatusNotFound)
			return
		}
	}
	v, err := h.plugins.RPC(r.Context(), r.PathValue("id"), r.PathValue("name"), body.ProjectID, body.Args)
	if err != nil {
		if errors.Is(err, plugins.ErrRPCRefused) {
			http.Error(w, plugins.Scrub(err.Error()), http.StatusForbidden)
			return
		}
		http.Error(w, plugins.Scrub(err.Error()), http.StatusUnprocessableEntity)
		return
	}
	jsonResp(w, http.StatusOK, v)
}

// pairPluginRunner makes the code an operator takes to a runner. It carries a secret, so it is only
// ever shown to the admin who asked; a new one revokes the last.
func (h *Handler) pairPluginRunner(w http.ResponseWriter, r *http.Request) {
	code, command, err := h.plugins.PairRunner(r.PathValue("runnerId"))
	if err != nil {
		pluginError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	jsonResp(w, http.StatusOK, map[string]string{"code": code, "command": command})
}

func (h *Handler) pluginRunners(w http.ResponseWriter, r *http.Request) {
	v, err := h.plugins.Runners(r.PathValue("id"))
	if err != nil {
		pluginError(w, err)
		return
	}
	jsonResp(w, http.StatusOK, v)
}

// ---- conversations ----

type pluginConversationRow struct {
	dbpkg.PluginConversation
	SessionExists bool   `json:"session_exists"`
	SessionKind   string `json:"session_kind,omitempty"`
	MessageCount  int    `json:"message_count"`
	Current       bool   `json:"current"`
}

func (h *Handler) listPluginConversations(w http.ResponseWriter, r *http.Request) {
	id, pid := r.PathValue("id"), r.URL.Query().Get("project")
	if _, err := h.plugins.Get(id); err != nil {
		pluginError(w, err)
		return
	}
	convs, err := dbpkg.ListPluginConversations(h.db, id, pid, r.URL.Query().Get("source"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	current, _ := dbpkg.GetOrchestratorSession(h.db, pid)
	out := make([]pluginConversationRow, 0, len(convs))
	for _, c := range convs {
		row := pluginConversationRow{PluginConversation: c}
		if s, err := dbpkg.GetSession(h.db, c.SessionID); err == nil {
			row.SessionExists, row.SessionKind, row.MessageCount = true, s.Kind, s.MessageCount
			row.Current = s.ID == current.ID
		}
		out = append(out, row)
	}
	jsonResp(w, http.StatusOK, out)
}

// pluginModel checks that a model belongs to this plugin and can take turns.
func (h *Handler) pluginModel(pluginID, model string) (string, error) {
	wire := model
	if !plugins.IsPluginModel(model) {
		wire = plugins.WireModel(pluginID, model)
	}
	if got, _, ok := plugins.ParseWireModel(wire); !ok || got != pluginID {
		return "", fmt.Errorf("the model %q does not belong to this plugin", model)
	}
	if err := h.plugins.CheckModel(wire); err != nil {
		return "", err
	}
	return wire, nil
}

func (h *Handler) newPluginSession(pluginID, projectID, wire string) (dbpkg.Session, error) {
	name := pluginID
	if v, err := h.plugins.Get(pluginID); err == nil {
		name = v.Name
	}
	sess := dbpkg.Session{
		ID: uuid.New().String(), ProjectID: projectID, Name: name, Model: wire,
		Status: string(sessionpkg.StatusIdle), Kind: dbpkg.SessionKindPlugin,
	}
	if err := dbpkg.InsertSession(h.db, sess); err != nil {
		return sess, err
	}
	return dbpkg.GetSession(h.db, sess.ID)
}

// newPluginConversation makes the chat a plugin's page embeds.
func (h *Handler) newPluginConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		ProjectID string `json:"projectId"`
		Model     string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if _, err := h.plugins.Get(id); err != nil {
		pluginError(w, err)
		return
	}
	if _, err := dbpkg.GetProject(h.db, body.ProjectID); err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	wire, err := h.pluginModel(id, body.Model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	sess, err := h.newPluginSession(id, body.ProjectID, wire)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusCreated, sess)
}

// continuePluginConversation brings a conversation back. Its chat is shown again; if the chat was
// deleted, a new one takes the conversation over: an orchestrator chat, the only kind the side panel
// finds again after a reload, or with ?surface=page a chat of the plugin's page.
func (h *Handler) continuePluginConversation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	v, err := h.plugins.Get(id)
	if err != nil {
		pluginError(w, err)
		return
	}
	c, err := dbpkg.GetPluginConversation(h.db, id, r.PathValue("cid"))
	if err != nil {
		http.Error(w, "conversation not found", http.StatusNotFound)
		return
	}
	sess, err := dbpkg.GetSession(h.db, c.SessionID)
	switch {
	case err == nil && sess.Kind == dbpkg.SessionKindOrchestrator:
		if h.manager != nil && !h.manager.TurnActive(sess.ID) {
			// the panel is about to show it: a running turn of the chat it replaces is stopped
			if cur, err := dbpkg.GetOrchestratorSession(h.db, sess.ProjectID); err == nil && cur.ID != sess.ID {
				h.manager.CancelTurn(cur.ID)
				h.manager.CloseLive(cur.ID)
			}
		}
		if err := dbpkg.MakeOrchestratorCurrent(h.db, sess.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		sess, _ = dbpkg.GetSession(h.db, sess.ID)
	case err == nil: // an ordinary chat: the UI opens it
	case errors.Is(err, sql.ErrNoRows) && r.URL.Query().Get("surface") != "page":
		if sess, err = h.createOrchestrator(c.ProjectID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := dbpkg.BindPluginConversation(h.db, id, c.ID, sess.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	case errors.Is(err, sql.ErrNoRows):
		wire := ""
		for _, def := range v.Manifest.Contributes.Conversations {
			if def.ID == c.Source {
				wire = plugins.WireModel(id, def.Model)
			}
		}
		if wire == "" && len(v.Manifest.Contributes.Models) > 0 {
			wire = plugins.WireModel(id, v.Manifest.Contributes.Models[0].ID)
		}
		if wire == "" {
			http.Error(w, "this plugin has no model to continue with", http.StatusConflict)
			return
		}
		if sess, err = h.newPluginSession(id, c.ProjectID, wire); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := dbpkg.BindPluginConversation(h.db, id, c.ID, sess.ID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	default:
		log.Printf("[plugins] continue %s: %v", c.ID, err)
		http.Error(w, "the chat could not be read", http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, sess)
}
