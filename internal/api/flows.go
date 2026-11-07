package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/flows"
	sessionpkg "claude-orchestrator/internal/session"

	"github.com/google/uuid"
)

// Flows: the project's automations. The engine is in internal/flows; this file gives it
// the board (flowHost) and gives the web its API. Everything a flow does goes through
// the same rules and side effects as a user or an agent doing it: a Run agent ends up on
// a runner like any other turn.

// StartFlows starts the engine. Only the hub calls it, and it needs no NATS of its own.
func (h *Handler) StartFlows(ctx context.Context) {
	h.flows = flows.NewEngine(h.db, flowHost{h})
	h.flows.EventTap = h.pluginEventTap
	h.manager.ToolGate = func(req sessionpkg.ToolRequest) (string, string) { return h.flowToolGate(ctx, req) }
	if projects, err := dbpkg.ListProjects(h.db); err == nil {
		for _, p := range projects {
			if err := flows.SeedBlueprints(h.db, p.ID); err != nil {
				log.Printf("[flows] seed project %s: %v", p.ID, err)
			}
		}
	}
	h.flows.Start(ctx)
}

// flowToolGate offers a tool request to the flows of the session's project. Any session counts,
// with or without a card. An answer other than allow or deny leaves it open.
func (h *Handler) flowToolGate(ctx context.Context, req sessionpkg.ToolRequest) (string, string) {
	sess, err := dbpkg.GetSession(h.db, req.SessionID)
	if err != nil {
		return flows.ToolAsk, ""
	}
	task, agentID := "", sess.AgentID
	if sess.CardID != "" {
		if card, err := dbpkg.GetKanbanCard(h.db, sess.CardID); err == nil {
			task = card.Title
			if agentID == "" {
				agentID = card.AgentID
			}
		}
	}
	agent := ""
	if agentID != "" {
		if a, err := dbpkg.GetAgent(h.db, agentID); err == nil {
			agent = a.Name
		}
	}
	v := h.flows.DecideTool(ctx, flows.ToolRequest{
		ProjectID: sess.ProjectID, CardID: sess.CardID, SessionID: req.SessionID, ToolUseID: req.ToolUseID,
		ToolName: req.ToolName, WorkDir: req.WorkDir, Task: task, Agent: agent, AutoMode: req.AutoMode, Input: req.Input,
	})
	if v.Decision == flows.ToolAsk && v.Reason != "" {
		log.Printf("[flows] no decision on %s of session %s: %s", req.ToolName, req.SessionID, v.Reason)
	}
	return v.Decision, v.Reason
}

// seedProjectFlows gives a new project its base flows.
func (h *Handler) seedProjectFlows(projectID string) {
	if h.plugins != nil {
		h.plugins.SeedProject(projectID)
	}
	if h.flows == nil {
		return
	}
	if err := flows.SeedBlueprints(h.db, projectID); err != nil {
		log.Printf("[flows] seed project %s: %v", projectID, err)
	}
}

// notifyFlows wakes the engine after something that may have logged an event.
func (h *Handler) notifyFlows() {
	if h.flows != nil {
		h.flows.Notify()
	}
}

// FlowTurnEnded logs the end of a turn on a card (Manager.OnTurnEnd).
func (h *Handler) FlowTurnEnded(sessID string) {
	h.recordTurnEvent(sessID, dbpkg.EventTurnEnded, nil)
}

func (h *Handler) recordTurnEvent(sessID, typ string, payload map[string]any) {
	sess, err := dbpkg.GetSession(h.db, sessID)
	if err != nil || sess.CardID == "" {
		return
	}
	if err := dbpkg.RecordFlowEvent(h.db, sess.ProjectID, typ, sess.CardID, sessID, payload); err != nil {
		return
	}
	h.notifyFlows()
}

// ---- host ----

type flowHost struct{ h *Handler }

func (f flowHost) CardScope(cardID string) (map[string]any, error) {
	c, err := dbpkg.GetKanbanCard(f.h.db, cardID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"id": c.ID, "title": c.Title, "description": c.Description, "type": c.CardType,
		"tags": c.Tags, "depends_on": c.DependsOn, "parent_id": c.ParentID, "project_id": c.ProjectID,
		"list": "", "kind": "", "agent": "", "agent_id": c.AgentID,
	}
	if l, err := dbpkg.GetKanbanList(f.h.db, c.ListID); err == nil {
		out["list"], out["kind"] = l.Name, l.Kind
	}
	if a, err := dbpkg.GetAgent(f.h.db, c.AgentID); err == nil {
		out["agent"] = a.Name
	}
	return out, nil
}

// MoveCard moves a card as a user's move would: the board's rules can refuse it, and
// entering In Progress starts the agent.
func (f flowHost) MoveCard(cardID, kind, listID string, cause dbpkg.EventCause) (string, error) {
	h := f.h
	card, err := dbpkg.GetKanbanCard(h.db, cardID)
	if err != nil {
		return "", err
	}
	if kind == dbpkg.KanbanReview {
		_ = dbpkg.EnsureReviewList(h.db, card.ProjectID) // a project older than the Review list
		h.reviewRowFor(card)
	}
	if listID == "" {
		l, err := dbpkg.FindKanbanListByKind(h.db, card.ProjectID, kind)
		if err != nil {
			return "", fmt.Errorf("this project has no %q list", kind)
		}
		listID = l.ID
	} else if l, err := dbpkg.GetKanbanList(h.db, listID); err != nil || l.ProjectID != card.ProjectID {
		return "", errors.New("the chosen list is not in this project")
	}
	if listID == card.ListID {
		return "", nil
	}
	old := card.ListID
	card.ListID = listID
	card.Position = h.endOf(card, listID)
	if reason := h.moveBlocker(card, old); reason != "" {
		return reason, nil
	}
	if err := dbpkg.UpdateKanbanCardCause(h.db, card, cause); err != nil {
		return "", err
	}
	if card, err = dbpkg.GetKanbanCard(h.db, card.ID); err != nil {
		return "", err
	}
	h.notifyTaskUpdated("", h.afterCardSaved(card))
	return "", nil
}

func (f flowHost) Comment(cardID, text string) error {
	card, err := dbpkg.GetKanbanCard(f.h.db, cardID)
	if err != nil {
		return err
	}
	if err := dbpkg.InsertCardComment(f.h.db, dbpkg.CardComment{
		ID: uuid.New().String(), CardID: cardID, Author: "lucode", Kind: "comment", Body: text,
	}); err != nil {
		return err
	}
	f.h.notifyTaskUpdated("", card)
	return nil
}

func (f flowHost) AddTag(cardID, tag string) error {
	card, err := dbpkg.GetKanbanCard(f.h.db, cardID)
	if err != nil {
		return err
	}
	if contains(card.Tags, tag) {
		return nil
	}
	card.Tags = append(card.Tags, tag)
	if err := dbpkg.UpdateKanbanCard(f.h.db, card); err != nil {
		return err
	}
	f.h.notifyTaskUpdated("", card)
	return nil
}

func (f flowHost) CreateTask(projectID, title, description, status, agent string, cause dbpkg.EventCause) (string, error) {
	h := f.h
	if strings.TrimSpace(title) == "" {
		return "", errors.New("Create task: the title came out empty")
	}
	agentID, err := h.agentByName(projectID, agent)
	if err != nil {
		return "", err
	}
	if status == "" {
		status = dbpkg.KanbanTodo
	}
	list, err := h.listByStatus(projectID, status)
	if err != nil {
		return "", err
	}
	card := dbpkg.KanbanCard{
		ID: uuid.New().String(), ProjectID: projectID, ListID: list.ID, Title: strings.TrimSpace(title),
		Description: description, CardType: "task", Tags: []string{}, DependsOn: []string{}, AgentID: agentID,
	}
	if err := dbpkg.InsertKanbanCardCause(h.db, card, cause); err != nil {
		return "", err
	}
	if card, err = dbpkg.GetKanbanCard(h.db, card.ID); err != nil {
		return "", err
	}
	if list.Kind == dbpkg.KanbanInProgress || list.Kind == dbpkg.KanbanDone {
		card = h.afterCardSaved(card)
	}
	h.notifyTaskUpdated("", card)
	return card.ID, nil
}

// RunAgent sends an instruction to the card's agent, in the card's session, on the
// agent's runner. The card goes to In Progress first without starting a second turn.
func (f flowHost) RunAgent(cardID, agent, prompt string, cause dbpkg.EventCause) (string, error) {
	h := f.h
	card, err := dbpkg.GetKanbanCard(h.db, cardID)
	if err != nil {
		return "", err
	}
	if agent != "" {
		id, err := h.agentByName(card.ProjectID, agent)
		if err != nil {
			return "", err
		}
		if id != "" && id != card.AgentID {
			card.AgentID = id
			if err := dbpkg.UpdateKanbanCard(h.db, card); err != nil {
				return "", err
			}
		}
	}
	if card.AgentID == "" {
		return "", errors.New("the card has no agent: assign one or choose it in the node")
	}
	if unmet := h.unmetDependencies(card); len(unmet) > 0 {
		return "", fmt.Errorf("the card is blocked by unfinished dependencies: %s", strings.Join(unmet, ", "))
	}
	// a flow restarts a blocked card under the same rule as a person
	target, err := dbpkg.FindKanbanListByKind(h.db, card.ProjectID, dbpkg.KanbanInProgress)
	if err != nil {
		return "", errors.New("this project has no In Progress list")
	}
	probe := card
	probe.ListID = target.ID
	if reason := h.moveBlocker(probe, card.ListID); reason != "" {
		return "", errors.New(reason)
	}
	// The chain has to be on record before the turn can end, even if the card is already in
	// progress and no move logs it.
	if err := dbpkg.RecordFlowChain(h.db, card.ProjectID, card.ID, cause); err != nil {
		return "", err
	}
	if card, err = h.moveCardToKindCause(card, dbpkg.KanbanInProgress, cause); err != nil {
		return "", err
	}
	if err := h.runTaskNote(card, prompt); err != nil {
		return "", err
	}
	h.notifyTaskUpdated("", card)
	sess, err := dbpkg.GetSessionByCard(h.db, card.ID)
	if err != nil {
		return "", fmt.Errorf("the card's session: %w", err)
	}
	return sess.ID, nil
}

func (f flowHost) RunChanged(run dbpkg.FlowRun) {
	if f.h.bus == nil {
		return
	}
	f.h.bus.Broadcast("", map[string]any{
		"type": "flow_updated", "project_id": run.ProjectID, "flow_id": run.FlowID, "run_id": run.ID, "status": run.Status,
	})
}

// ---- API ----

func (h *Handler) registerFlows(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/flow-node-types", func(w http.ResponseWriter, _ *http.Request) {
		jsonResp(w, http.StatusOK, flows.NodeTypes())
	})
	mux.HandleFunc("GET /api/flow-blueprints", func(w http.ResponseWriter, _ *http.Request) {
		jsonResp(w, http.StatusOK, flows.Blueprints())
	})
	mux.HandleFunc("GET /api/card-machine", func(w http.ResponseWriter, _ *http.Request) {
		jsonResp(w, http.StatusOK, flows.CardMachine())
	})
	mux.HandleFunc("GET /api/projects/{pid}/flows", h.listFlows)
	mux.HandleFunc("POST /api/projects/{pid}/flows", h.createFlow)
	mux.HandleFunc("GET /api/projects/{pid}/flow-runs/active", h.listActiveFlowRuns)
	mux.HandleFunc("GET /api/projects/{pid}/flow-approvals", h.listFlowApprovals)
	mux.HandleFunc("POST /api/flow-steps/{seq}/decision", h.decideFlowApproval)
	mux.HandleFunc("GET /api/flow-ai-settings", h.getFlowAISettings)
	mux.HandleFunc("PUT /api/flow-ai-settings", h.putFlowAISettings)
	h.registerFlowPush(mux)
	mux.HandleFunc("GET /api/projects/{pid}/flows/paused", h.getFlowsPaused)
	mux.HandleFunc("PUT /api/projects/{pid}/flows/paused", h.setFlowsPaused)
	mux.HandleFunc("GET /api/flows/{id}", h.getFlow)
	mux.HandleFunc("PUT /api/flows/{id}", h.saveFlow)
	mux.HandleFunc("DELETE /api/flows/{id}", h.deleteFlow)
	mux.HandleFunc("POST /api/flows/{id}/validate", h.validateFlow)
	mux.HandleFunc("POST /api/flows/{id}/publish", h.publishFlow)
	mux.HandleFunc("PUT /api/flows/{id}/enabled", h.setFlowEnabled)
	mux.HandleFunc("POST /api/flows/{id}/run", h.runFlow)
	mux.HandleFunc("POST /api/flows/{id}/fork", h.forkFlow)
	mux.HandleFunc("GET /api/flows/{id}/runs", h.listFlowRuns)
	mux.HandleFunc("GET /api/flow-runs/{id}", h.getFlowRun)
	mux.HandleFunc("POST /api/flow-runs/{id}/cancel", h.cancelFlowRun)
}

func (h *Handler) listFlows(w http.ResponseWriter, r *http.Request) {
	list, err := dbpkg.ListFlows(h.db, r.PathValue("pid"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, list)
}

func (h *Handler) createFlow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string `json:"name"`
		Blueprint string `json:"blueprint"` // start from a base flow, as an unpublished draft
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	var draft json.RawMessage
	if body.Blueprint != "" {
		b, ok := flows.BlueprintOf(body.Blueprint)
		if !ok {
			http.Error(w, "unknown blueprint", http.StatusBadRequest)
			return
		}
		draft, _ = json.Marshal(b.Graph)
	}
	f, err := dbpkg.InsertFlow(h.db, dbpkg.Flow{ProjectID: r.PathValue("pid"), Name: strings.TrimSpace(body.Name), Draft: draft})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusCreated, f)
}

func (h *Handler) flowFor(w http.ResponseWriter, r *http.Request) (dbpkg.Flow, bool) {
	f, err := dbpkg.GetFlow(h.db, r.PathValue("id"))
	if err != nil {
		http.Error(w, "flow not found", http.StatusNotFound)
		return f, false
	}
	return f, true
}

func (h *Handler) getFlow(w http.ResponseWriter, r *http.Request) {
	if f, ok := h.flowFor(w, r); ok {
		jsonResp(w, http.StatusOK, f)
	}
}

// saveFlow stores the draft. A draft may be unfinished; only publishing validates it.
func (h *Handler) saveFlow(w http.ResponseWriter, r *http.Request) {
	f, ok := h.flowFor(w, r)
	if !ok {
		return
	}
	var body struct {
		Name  string          `json:"name"`
		Draft json.RawMessage `json:"draft"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = f.Name
	}
	draft := f.Draft
	if len(body.Draft) > 0 {
		if _, err := flows.ParseGraph(body.Draft); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		draft = body.Draft
	}
	if err := dbpkg.UpdateFlowDraft(h.db, f.ID, name, draft); err != nil {
		http.Error(w, err.Error(), flowSaveStatus(err))
		return
	}
	f, _ = dbpkg.GetFlow(h.db, f.ID)
	jsonResp(w, http.StatusOK, f)
}

func (h *Handler) deleteFlow(w http.ResponseWriter, r *http.Request) {
	if f, err := dbpkg.GetFlow(h.db, r.PathValue("id")); err == nil && f.Locked {
		http.Error(w, "this flow belongs to a plugin: it goes away with the plugin. Switch it off to stop it.", http.StatusConflict)
		return
	}
	if err := dbpkg.DeleteFlow(h.db, r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) validateFlow(w http.ResponseWriter, r *http.Request) {
	f, ok := h.flowFor(w, r)
	if !ok {
		return
	}
	g, err := flows.ParseGraph(f.Draft)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"problems": flows.Validate(g)})
}

// publishFlow freezes the draft as a new version. Runs in progress finish on the version
// they started with.
func (h *Handler) publishFlow(w http.ResponseWriter, r *http.Request) {
	f, ok := h.flowFor(w, r)
	if !ok {
		return
	}
	if f.Locked {
		http.Error(w, dbpkg.ErrFlowLocked.Error(), http.StatusConflict)
		return
	}
	g, err := flows.ParseGraph(f.Draft)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if problems := flows.Validate(g); len(problems) > 0 {
		jsonResp(w, http.StatusUnprocessableEntity, map[string]any{"problems": problems})
		return
	}
	if _, err := dbpkg.PublishFlow(h.db, f.ID, f.Draft); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f, _ = dbpkg.GetFlow(h.db, f.ID)
	h.notifyFlows()
	jsonResp(w, http.StatusOK, f)
}

func (h *Handler) setFlowEnabled(w http.ResponseWriter, r *http.Request) {
	f, ok := h.flowFor(w, r)
	if !ok {
		return
	}
	if refuseLockedFlowRPC(w, r, f) {
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if body.Enabled && f.PublishedVersion == 0 {
		http.Error(w, "publish the flow first", http.StatusConflict)
		return
	}
	if err := dbpkg.SetFlowEnabled(h.db, f.ID, body.Enabled); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f, _ = dbpkg.GetFlow(h.db, f.ID)
	jsonResp(w, http.StatusOK, f)
}

func (h *Handler) runFlow(w http.ResponseWriter, r *http.Request) {
	f, ok := h.flowFor(w, r)
	if !ok {
		return
	}
	if refuseLockedFlowRPC(w, r, f) {
		return
	}
	var body struct {
		CardID string `json:"card_id"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.CardID != "" {
		c, err := dbpkg.GetKanbanCard(h.db, body.CardID)
		if err != nil || c.ProjectID != f.ProjectID {
			http.Error(w, "that card is not in this project", http.StatusBadRequest)
			return
		}
	}
	if h.flows == nil {
		http.Error(w, "the flow engine is not running on this instance", http.StatusServiceUnavailable)
		return
	}
	run, err := h.flows.StartManual(f, body.CardID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	jsonResp(w, http.StatusCreated, run)
}

func (h *Handler) listFlowRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	runs, err := dbpkg.ListFlowRuns(h.db, r.PathValue("id"), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, runs)
}

// listActiveFlowRuns is the Running tab's summary: which flows are on which node, by name.
func (h *Handler) listActiveFlowRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := dbpkg.ListActiveFlowRuns(h.db, r.PathValue("pid"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type row struct {
		dbpkg.ActiveFlowRun
		CardTitle string   `json:"card_title"`
		OpenNames []string `json:"open_names"`
	}
	out := make([]row, 0, len(runs))
	for _, a := range runs {
		item := row{ActiveFlowRun: a, OpenNames: []string{}}
		if c, err := dbpkg.GetKanbanCard(h.db, a.Run.CardID); err == nil {
			item.CardTitle = c.Title
		}
		if raw, err := dbpkg.GetFlowVersion(h.db, a.Run.FlowID, a.Run.Version); err == nil {
			if g, err := flows.ParseGraph(raw); err == nil {
				for _, id := range a.Open {
					item.OpenNames = append(item.OpenNames, flowNodeTitle(g, id))
				}
			}
		}
		out = append(out, item)
	}
	jsonResp(w, http.StatusOK, out)
}

func flowNodeTitle(g flows.Graph, id string) string {
	for _, n := range g.Nodes {
		if n.ID == id {
			if def, ok := flows.NodeTypeOf(n.Type); ok {
				return def.Title
			}
		}
	}
	return id
}

func (h *Handler) getFlowRun(w http.ResponseWriter, r *http.Request) {
	run, err := dbpkg.GetFlowRun(h.db, r.PathValue("id"))
	if err != nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	steps, err := dbpkg.ListFlowSteps(h.db, run.ID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	graph, err := dbpkg.GetFlowVersion(h.db, run.FlowID, run.Version)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"run": run, "steps": steps, "graph": graph})
}

func (h *Handler) cancelFlowRun(w http.ResponseWriter, r *http.Request) {
	if h.flows == nil {
		http.Error(w, "the flow engine is not running on this instance", http.StatusServiceUnavailable)
		return
	}
	if err := h.flows.Cancel(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) getFlowsPaused(w http.ResponseWriter, r *http.Request) {
	jsonResp(w, http.StatusOK, map[string]bool{"paused": dbpkg.FlowsPaused(h.db, r.PathValue("pid"))})
}

// setFlowsPaused is the project's off switch: no new runs, and running ones hold their steps.
func (h *Handler) setFlowsPaused(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Paused bool `json:"paused"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if err := dbpkg.SetFlowsPaused(h.db, r.PathValue("pid"), body.Paused); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.notifyFlows()
	jsonResp(w, http.StatusOK, map[string]bool{"paused": body.Paused})
}
