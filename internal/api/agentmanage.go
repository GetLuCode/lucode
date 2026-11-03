package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/taskmcp"

	"github.com/google/uuid"
)

// Agent management tools: an agent creates, edits and deletes the agents of its
// own project. Guard rails, because an agent that can make agents could otherwise
// hand itself more power:
//   - a new or edited agent never gets a built-in or lucode tool its creator does not have;
//   - the agent-management tools themselves are not inherited unless asked for (and allowed);
//   - only the project's own agents are touched: never global ones, never itself;
//   - the environment (runner, AI account) is not exposed.

var agentManageTools = []string{taskmcp.ToolAgentCreate, taskmcp.ToolAgentUpdate, taskmcp.ToolAgentDelete}

// defaultBuiltinTools is what an agent gets when nobody says which built-in tools.
var defaultBuiltinTools = []string{"Bash", "Edit", "Read", "Write"}

func isMiniMax(model string) bool { return strings.HasPrefix(model, "MiniMax") }

// callerLucodeTools is the set of lucode tools the calling agent has.
func callerLucodeTools(agent dbpkg.Agent) []string {
	if agent.LucodeTools == nil {
		return append(taskmcp.Names(), taskmcp.BrowserNames()...)
	}
	return agent.LucodeTools
}

// capTools checks that every wanted tool is one the creator has. limit == nil means
// no limit (the creator has every built-in tool).
func capTools(kind string, want, limit []string) error {
	if limit == nil {
		return nil
	}
	has := map[string]bool{}
	for _, t := range limit {
		has[t] = true
	}
	var extra []string
	for _, t := range want {
		if !has[t] {
			extra = append(extra, t)
		}
	}
	if len(extra) > 0 {
		return fmt.Errorf("you cannot give %s you do not have: %s", kind, strings.Join(extra, ", "))
	}
	return nil
}

// builtinLimit is the built-in tools the caller may hand out (nil = any).
func builtinLimit(agent dbpkg.Agent) []string {
	if len(agent.Tools) == 0 {
		return nil
	}
	return agent.Tools
}

// namedAgent finds an agent of the caller's project by name or id. Global agents are
// visible to the caller but never editable.
func (h *Handler) namedAgent(projectID, ref string) (dbpkg.Agent, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return dbpkg.Agent{}, errors.New("agent is required: the name of the agent")
	}
	agents, err := dbpkg.ListAgents(h.db, projectID)
	if err != nil {
		return dbpkg.Agent{}, err
	}
	var names []string
	for _, a := range agents {
		if a.ID == ref || strings.EqualFold(a.Name, ref) {
			return a, nil
		}
		names = append(names, a.Name)
	}
	return dbpkg.Agent{}, fmt.Errorf("no agent named %q; agents of this project: %s", ref, orNone(strings.Join(names, ", ")))
}

// editable returns the agent if the caller may change it.
func (h *Handler) editableAgent(caller dbpkg.Agent, ref string) (dbpkg.Agent, error) {
	a, err := h.namedAgent(caller.ProjectID, ref)
	if err != nil {
		return a, err
	}
	switch {
	case a.ID == caller.ID:
		return a, errors.New("you cannot change or delete yourself")
	case a.ProjectID == "":
		return a, fmt.Errorf("%s is a global agent: only the user can change it", a.Name)
	case a.ProjectID != caller.ProjectID:
		return a, fmt.Errorf("agent %q is not in this project", a.Name)
	}
	return a, nil
}

// supervisorID resolves reports_to: an agent name, "me" for the caller, or ""/"none".
func (h *Handler) supervisorID(caller dbpkg.Agent, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	switch strings.ToLower(ref) {
	case "", "none":
		return "", nil
	case "me", "myself":
		return caller.ID, nil
	}
	a, err := h.namedAgent(caller.ProjectID, ref)
	if err != nil {
		return "", fmt.Errorf("reports_to: %w", err)
	}
	return a.ID, nil
}

func (h *Handler) checkModel(model, executor string) error {
	if strings.TrimSpace(model) == "" {
		return errors.New("model is required")
	}
	if executor == "claude" && isMiniMax(model) {
		return errors.New("MiniMax models run through executor \"aiapi\", not \"claude\"")
	}
	return nil
}

func (h *Handler) agentNameTaken(projectID, name, exceptID string) bool {
	agents, _ := dbpkg.ListAgents(h.db, projectID)
	for _, a := range agents {
		if a.ID != exceptID && strings.EqualFold(a.Name, name) {
			return true
		}
	}
	return false
}

// agentCreate is the agent_create tool.
func (h *Handler) agentCreate(caller dbpkg.Agent, args json.RawMessage) (string, error) {
	var a struct {
		Name        string   `json:"name"`
		Personality string   `json:"personality"`
		Model       string   `json:"model"`
		Executor    string   `json:"executor"`
		Persistent  bool     `json:"persistent"`
		Tools       []string `json:"tools"`
		LucodeTools []string `json:"lucode_tools"`
		ReportsTo   string   `json:"reports_to"`
	}
	_ = json.Unmarshal(args, &a)
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return "", errors.New("name is required")
	}
	if h.agentNameTaken(caller.ProjectID, name, "") {
		return "", fmt.Errorf("an agent named %q already exists", name)
	}
	executor := "claude"
	if a.Executor != "" {
		executor = a.Executor
	}
	if executor != "claude" && executor != "aiapi" {
		return "", errors.New(`executor must be "claude" or "aiapi"`)
	}
	model := orDefault(a.Model, "sonnet")
	if err := h.checkModel(model, executor); err != nil {
		return "", err
	}

	// Built-in tools: asked ones must be the caller's; by default the usual four, within the caller's.
	tools := a.Tools
	if len(tools) == 0 {
		tools = defaultBuiltinTools
		if lim := builtinLimit(caller); lim != nil {
			tools = lim
		}
	}
	if err := capTools("built-in tools", tools, builtinLimit(caller)); err != nil {
		return "", err
	}

	// lucode tools: asked ones must be the caller's; by default its own minus agent management.
	var lucodeTools []string
	if a.LucodeTools != nil {
		lucodeTools = sanitizeLucodeTools(a.LucodeTools)
		if err := capTools("lucode tools", lucodeTools, callerLucodeTools(caller)); err != nil {
			return "", err
		}
	} else {
		lucodeTools = []string{}
		for _, t := range callerLucodeTools(caller) {
			// browser control is never handed down by default: it has to be asked for
			if !contains(agentManageTools, t) && !taskmcp.IsBrowserTool(t) {
				lucodeTools = append(lucodeTools, t)
			}
		}
	}

	reportsTo, err := h.supervisorID(caller, a.ReportsTo)
	if err != nil {
		return "", err
	}
	if err := h.validateReportsTo("", caller.ProjectID, reportsTo); err != nil {
		return "", fmt.Errorf("reports_to: %w", err)
	}
	ag := dbpkg.Agent{
		ID: uuid.New().String(), ProjectID: caller.ProjectID, Name: name,
		Model: model, Executor: executor, Persistent: a.Persistent && executor == "claude",
		ReportsTo: reportsTo, LucodeTools: lucodeTools, Personality: a.Personality, Tools: tools,
	}
	if err := dbpkg.InsertAgent(h.db, ag); err != nil {
		return "", err
	}
	return fmt.Sprintf("Created agent %q (id %s): model %s, executor %s, tools %s, lucode tools %s.",
		ag.Name, ag.ID, ag.Model, ag.Executor, strings.Join(ag.Tools, ","), orNone(strings.Join(ag.LucodeTools, ","))), nil
}

// agentUpdate is the agent_update tool: only the fields given change.
func (h *Handler) agentUpdate(caller dbpkg.Agent, args json.RawMessage) (string, error) {
	var a struct {
		Agent       string    `json:"agent"`
		Name        *string   `json:"name"`
		Personality *string   `json:"personality"`
		Model       *string   `json:"model"`
		Executor    *string   `json:"executor"`
		Persistent  *bool     `json:"persistent"`
		Tools       *[]string `json:"tools"`
		LucodeTools *[]string `json:"lucode_tools"`
		ReportsTo   *string   `json:"reports_to"`
	}
	_ = json.Unmarshal(args, &a)
	ag, err := h.editableAgent(caller, a.Agent)
	if err != nil {
		return "", err
	}
	var changed []string
	if a.Name != nil {
		n := strings.TrimSpace(*a.Name)
		if n == "" {
			return "", errors.New("name cannot be empty")
		}
		if h.agentNameTaken(caller.ProjectID, n, ag.ID) {
			return "", fmt.Errorf("an agent named %q already exists", n)
		}
		ag.Name = n
		changed = append(changed, "name")
	}
	if a.Personality != nil {
		ag.Personality = *a.Personality
		changed = append(changed, "personality")
	}
	if a.Executor != nil {
		if *a.Executor != "claude" && *a.Executor != "aiapi" {
			return "", errors.New(`executor must be "claude" or "aiapi"`)
		}
		ag.Executor = *a.Executor
		changed = append(changed, "executor")
	}
	if a.Model != nil {
		ag.Model = strings.TrimSpace(*a.Model)
		changed = append(changed, "model")
	}
	if err := h.checkModel(ag.Model, ag.Executor); err != nil {
		return "", err
	}
	if a.Persistent != nil {
		ag.Persistent = *a.Persistent
		changed = append(changed, "persistent")
	}
	ag.Persistent = ag.Persistent && ag.Executor == "claude"
	if a.Tools != nil {
		if len(*a.Tools) == 0 {
			return "", errors.New("tools cannot be empty: list the built-in tools the agent may use")
		}
		if err := capTools("built-in tools", *a.Tools, builtinLimit(caller)); err != nil {
			return "", err
		}
		ag.Tools = *a.Tools
		changed = append(changed, "tools")
	}
	if a.LucodeTools != nil {
		lt := sanitizeLucodeTools(*a.LucodeTools)
		if err := capTools("lucode tools", lt, callerLucodeTools(caller)); err != nil {
			return "", err
		}
		ag.LucodeTools = lt
		changed = append(changed, "lucode_tools")
	}
	if a.ReportsTo != nil {
		sup, err := h.supervisorID(caller, *a.ReportsTo)
		if err != nil {
			return "", err
		}
		if err := h.validateReportsTo(ag.ID, ag.ProjectID, sup); err != nil {
			return "", fmt.Errorf("reports_to: %w", err)
		}
		ag.ReportsTo = sup
		changed = append(changed, "reports_to")
	}
	if len(changed) == 0 {
		return "", errors.New("nothing to change: give at least one field to update")
	}
	if err := dbpkg.UpdateAgent(h.db, ag); err != nil {
		return "", err
	}
	h.closeLives(ag.ID) // live processes keep the old configuration; restart them
	return fmt.Sprintf("Updated agent %q (%s).", ag.Name, strings.Join(changed, ", ")), nil
}

// agentDelete is the agent_delete tool. It refuses while the agent is working or
// others report to it, so no work or reporting line is cut off silently.
func (h *Handler) agentDelete(caller dbpkg.Agent, args json.RawMessage) (string, error) {
	var a struct {
		Agent string `json:"agent"`
	}
	_ = json.Unmarshal(args, &a)
	ag, err := h.editableAgent(caller, a.Agent)
	if err != nil {
		return "", err
	}
	if h.agentBusy(caller.ProjectID, ag.ID) {
		return "", fmt.Errorf("%s is working right now (a task in progress or a running session); wait for it or move its task first", ag.Name)
	}
	all, _ := dbpkg.ListAgents(h.db, caller.ProjectID)
	var subs []string
	for _, o := range all {
		if o.ReportsTo == ag.ID {
			subs = append(subs, o.Name)
		}
	}
	if len(subs) > 0 {
		return "", fmt.Errorf("%s still has agents reporting to it (%s): reassign them first with agent_update", ag.Name, strings.Join(subs, ", "))
	}
	h.closeLives(ag.ID)
	if err := dbpkg.UnassignAgentCards(h.db, ag.ID); err != nil {
		return "", err
	}
	if err := dbpkg.DeleteAgent(h.db, ag.ID); err != nil {
		return "", err
	}
	return fmt.Sprintf("Deleted agent %q. Its tasks are now unassigned; give them a new agent with task_update.", ag.Name), nil
}
