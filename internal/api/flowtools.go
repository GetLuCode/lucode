package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/flows"
	"claude-orchestrator/internal/taskmcp"
)

// Flow tools: an agent prepares automations with the user. It reads flows and writes drafts,
// and never publishes, enables or deletes one: a published flow can approve tools or merge
// branches, so publishing is what stops an agent from raising its own limits. Only flows of the
// agent's project are reachable. See taskmcp/flow.go.

func (h *Handler) handleFlowTool(projectID, author, tool string, args json.RawMessage) (string, error) {
	switch tool {
	case taskmcp.ToolFlowList:
		return h.flowListTool(projectID)
	case taskmcp.ToolFlowGet:
		return h.flowGetTool(projectID, args)
	case taskmcp.ToolFlowCatalog:
		return flowCatalogTool()
	case taskmcp.ToolFlowCreate:
		return h.flowCreateTool(projectID, author, args)
	case taskmcp.ToolFlowUpdate:
		return h.flowUpdateTool(projectID, author, args)
	}
	return "", fmt.Errorf("unknown flow tool %q", tool)
}

// projectFlow finds a flow of the project by id or name.
func (h *Handler) projectFlow(projectID, ref string) (dbpkg.Flow, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return dbpkg.Flow{}, errors.New("flow is required: its id or name (see flow_list)")
	}
	list, err := dbpkg.ListFlows(h.db, projectID)
	if err != nil {
		return dbpkg.Flow{}, err
	}
	var named []dbpkg.Flow
	for _, f := range list {
		if f.ID == ref {
			return f, nil
		}
		if strings.EqualFold(f.Name, ref) {
			named = append(named, f)
		}
	}
	switch len(named) {
	case 1:
		return named[0], nil
	case 0:
		return dbpkg.Flow{}, fmt.Errorf("no flow %q in this project (see flow_list)", ref)
	}
	return dbpkg.Flow{}, fmt.Errorf("%d flows are named %q: use the id (see flow_list)", len(named), ref)
}

// flowGraphArg reads a graph an agent wrote. A draft may be unfinished, but it must never hold a
// node the editor cannot draw (it would lose it on its next save), and a graph that forgot its
// nodes must not wipe a draft.
func flowGraphArg(raw json.RawMessage) (json.RawMessage, flows.Graph, error) {
	g, err := flows.ParseGraph(raw)
	if err != nil {
		return nil, g, err
	}
	if g.Nodes == nil {
		return nil, g, errors.New(`the graph has no "nodes": it replaces the whole draft, so send every node`)
	}
	for _, n := range g.Nodes {
		if _, ok := flows.NodeTypeOf(n.Type); !ok {
			return nil, g, fmt.Errorf("unknown node type %q (see flow_catalog)", n.Type)
		}
	}
	if g.Edges == nil {
		g.Edges = []flows.Edge{}
	}
	clean, err := json.Marshal(g)
	return clean, g, err
}

// flowDraftState tells the agent where a draft stands, always ending on what it cannot do.
func flowDraftState(f dbpkg.Flow, g flows.Graph) string {
	var sb strings.Builder
	if problems := flows.Validate(g); len(problems) > 0 {
		sb.WriteString("Publishing would fail until these are fixed:\n")
		for _, p := range problems {
			if p.NodeID != "" {
				fmt.Fprintf(&sb, "- %s: %s\n", p.NodeID, p.Message)
			} else {
				fmt.Fprintf(&sb, "- %s\n", p.Message)
			}
		}
	} else {
		sb.WriteString("The draft is valid.\n")
	}
	sb.WriteString("It runs nothing until the user publishes it in the editor.")
	if f.PublishedVersion > 0 {
		fmt.Fprintf(&sb, " Version %d stays in place until then.", f.PublishedVersion)
	}
	return sb.String()
}

// flowDraftChanged shows an agent's change in any editor that has the flow open.
func (h *Handler) flowDraftChanged(f dbpkg.Flow, author string) {
	if h.bus == nil {
		return
	}
	h.bus.Broadcast("", map[string]any{"type": "flow_draft_updated", "project_id": f.ProjectID, "flow_id": f.ID, "agent": author})
}

func (h *Handler) flowListTool(projectID string) (string, error) {
	list, err := dbpkg.ListFlows(h.db, projectID)
	if err != nil {
		return "", err
	}
	if len(list) == 0 {
		return "This project has no flows.", nil
	}
	var sb strings.Builder
	for _, f := range list {
		state := "draft only"
		if f.PublishedVersion > 0 {
			state = fmt.Sprintf("v%d, off", f.PublishedVersion)
			if f.Enabled {
				state = fmt.Sprintf("v%d, on", f.PublishedVersion)
			}
		}
		fmt.Fprintf(&sb, "%s: %s (%s)", f.ID, f.Name, state)
		if f.DraftAgent != "" {
			fmt.Fprintf(&sb, ", draft edited by %s and not published", f.DraftAgent)
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

func (h *Handler) flowGetTool(projectID string, args json.RawMessage) (string, error) {
	var a struct {
		Flow string `json:"flow"`
	}
	_ = json.Unmarshal(args, &a)
	f, err := h.projectFlow(projectID, a.Flow)
	if err != nil {
		return "", err
	}
	g, err := flows.ParseGraph(f.Draft)
	if err != nil {
		return "", err
	}
	body, err := json.MarshalIndent(map[string]any{
		"id": f.ID, "name": f.Name, "published_version": f.PublishedVersion, "enabled": f.Enabled,
		"draft_edited_by": f.DraftAgent, "draft": g,
	}, "", " ")
	if err != nil {
		return "", err
	}
	return string(body) + "\n\n" + flowDraftState(f, g), nil
}

func flowCatalogTool() (string, error) {
	type blueprint struct {
		Key         string `json:"key"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	var bps []blueprint
	for _, b := range flows.Blueprints() {
		bps = append(bps, blueprint{b.Key, b.Name, b.Description})
	}
	body, err := json.MarshalIndent(map[string]any{
		"expressions": "If, Switch and {{templates}} are CEL over card, event, run and nodes.<id>.<output field>.",
		"node_types":  flows.NodeTypes(),
		"blueprints":  bps,
	}, "", " ")
	return string(body), err
}

func (h *Handler) flowCreateTool(projectID, author string, args json.RawMessage) (string, error) {
	var a struct {
		Name      string          `json:"name"`
		Blueprint string          `json:"blueprint"`
		Graph     json.RawMessage `json:"graph"`
	}
	_ = json.Unmarshal(args, &a)
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return "", errors.New("name is required")
	}
	if a.Blueprint != "" && len(a.Graph) > 0 {
		return "", errors.New("give a blueprint or a graph, not both")
	}
	draft := json.RawMessage(`{"nodes":[],"edges":[]}`)
	switch {
	case a.Blueprint != "":
		b, ok := flows.BlueprintOf(a.Blueprint)
		if !ok {
			return "", fmt.Errorf("unknown blueprint %q (see flow_catalog)", a.Blueprint)
		}
		raw, err := json.Marshal(b.Graph)
		if err != nil {
			return "", err
		}
		draft = raw
	case len(a.Graph) > 0:
		clean, _, err := flowGraphArg(a.Graph)
		if err != nil {
			return "", err
		}
		draft = clean
	}
	f, err := dbpkg.InsertFlow(h.db, dbpkg.Flow{ProjectID: projectID, Name: name, Draft: draft})
	if err != nil {
		return "", err
	}
	if err := dbpkg.SaveFlowDraftByAgent(h.db, f.ID, name, draft, author); err != nil {
		return "", err
	}
	h.flowDraftChanged(f, author)
	g, err := flows.ParseGraph(draft)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Created flow %q (id %s).\n%s", name, f.ID, flowDraftState(f, g)), nil
}

func (h *Handler) flowUpdateTool(projectID, author string, args json.RawMessage) (string, error) {
	var a struct {
		Flow  string          `json:"flow"`
		Name  string          `json:"name"`
		Graph json.RawMessage `json:"graph"`
	}
	_ = json.Unmarshal(args, &a)
	f, err := h.projectFlow(projectID, a.Flow)
	if err != nil {
		return "", err
	}
	name, draft := strings.TrimSpace(a.Name), f.Draft
	if name == "" {
		name = f.Name
	}
	if len(a.Graph) == 0 && name == f.Name {
		return "", errors.New("nothing to change: give a graph or a new name")
	}
	if len(a.Graph) > 0 {
		if draft, _, err = flowGraphArg(a.Graph); err != nil {
			return "", err
		}
	}
	if err := dbpkg.SaveFlowDraftByAgent(h.db, f.ID, name, draft, author); err != nil {
		return "", err
	}
	h.flowDraftChanged(f, author)
	g, err := flows.ParseGraph(draft)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Updated the draft of %q.\n%s", name, flowDraftState(f, g)), nil
}
