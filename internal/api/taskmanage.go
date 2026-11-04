package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	dbpkg "claude-orchestrator/internal/db"

	"github.com/google/uuid"
)

// Task management tools: an agent lists, creates and updates the tasks of its
// project. They go through the same workflow as the board: a task that enters
// "In Progress" starts its agent (or is parked in Blocked), one that enters
// "Done" releases the tasks that depended on it.

const maxTaskListLimit = 100

// listByStatus finds a list of the project from a status the agent typed: a
// workflow name (backlog, todo, in_progress, done, blocked) or the name of a list.
func (h *Handler) listByStatus(projectID, status string) (dbpkg.KanbanList, error) {
	lists, err := dbpkg.ListKanbanLists(h.db, projectID)
	if err != nil {
		return dbpkg.KanbanList{}, err
	}
	key := strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.ToLower(strings.TrimSpace(status)))
	var names []string
	for _, l := range lists {
		names = append(names, l.Name)
		nameKey := strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.ToLower(l.Name))
		if (l.Kind != "" && l.Kind == key) || nameKey == key {
			return l, nil
		}
	}
	return dbpkg.KanbanList{}, fmt.Errorf("unknown status %q; the lists of this project are: %s", status, strings.Join(names, ", "))
}

// agentByName resolves an agent of the project (or a global one) from its name.
// "" and "none" mean no agent.
func (h *Handler) agentByName(projectID, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "none") || strings.EqualFold(name, "unassigned") {
		return "", nil
	}
	agents, err := dbpkg.ListAgents(h.db, projectID)
	if err != nil {
		return "", err
	}
	var names []string
	for _, a := range agents {
		if strings.EqualFold(a.Name, name) || a.ID == name {
			return a.ID, nil
		}
		names = append(names, a.Name)
	}
	return "", fmt.Errorf("no agent named %q; agents of this project: %s", name, orNone(strings.Join(names, ", ")))
}

// projectTask returns a task of the caller's project.
func (h *Handler) projectTask(caller dbpkg.Session, id string) (dbpkg.KanbanCard, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return dbpkg.KanbanCard{}, errors.New("task_id is required")
	}
	c, err := dbpkg.GetKanbanCard(h.db, id)
	if err != nil || c.ProjectID != caller.ProjectID {
		return dbpkg.KanbanCard{}, fmt.Errorf("task %q not found in this project", id)
	}
	return c, nil
}

// checkRelations validates depends_on / parent: tasks of the project, never the task itself.
func (h *Handler) checkRelations(caller dbpkg.Session, selfID string, deps []string, parent string) error {
	for _, d := range deps {
		if d == selfID {
			return errors.New("a task cannot depend on itself")
		}
		if _, err := h.projectTask(caller, d); err != nil {
			return fmt.Errorf("depends_on: %w", err)
		}
	}
	if parent != "" {
		if parent == selfID {
			return errors.New("a task cannot be its own parent")
		}
		if _, err := h.projectTask(caller, parent); err != nil {
			return fmt.Errorf("parent_id: %w", err)
		}
	}
	return nil
}

// endOf is the position after the last card of a list.
func (h *Handler) endOf(card dbpkg.KanbanCard, listID string) int {
	pos := 0
	if all, err := dbpkg.ListKanbanCards(h.db, card.ProjectID, false); err == nil {
		for _, c := range all {
			if c.ListID == listID && c.ID != card.ID && c.Position >= pos {
				pos = c.Position + 1
			}
		}
	}
	return pos
}

// taskList is the task_list tool: the tasks of the project, optionally one status.
func (h *Handler) taskList(caller dbpkg.Session, args json.RawMessage) (string, error) {
	var a struct {
		Status string `json:"status"`
		Limit  int    `json:"limit"`
	}
	_ = json.Unmarshal(args, &a)
	lists, err := dbpkg.ListKanbanLists(h.db, caller.ProjectID)
	if err != nil {
		return "", err
	}
	var only string
	if strings.TrimSpace(a.Status) != "" {
		l, err := h.listByStatus(caller.ProjectID, a.Status)
		if err != nil {
			return "", err
		}
		only = l.ID
	}
	cards, err := dbpkg.ListKanbanCards(h.db, caller.ProjectID, false)
	if err != nil {
		return "", err
	}
	if a.Limit <= 0 || a.Limit > maxTaskListLimit {
		a.Limit = maxTaskListLimit
	}
	var sb strings.Builder
	n := 0
	for _, l := range lists {
		if only != "" && l.ID != only {
			continue
		}
		for _, c := range cards {
			if c.ListID != l.ID {
				continue
			}
			if n == a.Limit {
				fmt.Fprintf(&sb, "… more tasks not shown (limit %d); filter by status.\n", a.Limit)
				return strings.TrimSpace(sb.String()), nil
			}
			agent := ""
			if c.AgentID != "" {
				if ag, err := dbpkg.GetAgent(h.db, c.AgentID); err == nil {
					agent = ", agent: " + ag.Name
				}
			}
			fmt.Fprintf(&sb, "- %s [%s] %s (%s%s)\n", c.ID, l.Name, c.Title, c.CardType, agent)
			n++
		}
	}
	if n == 0 {
		return "No tasks.", nil
	}
	return strings.TrimSpace(sb.String()), nil
}

// taskCreate is the task_create tool. Without a status it lands in Todo, or in In
// Progress when its agent is free and nothing blocks it (see defaultCreateStatus).
func (h *Handler) taskCreate(sessID string, caller dbpkg.Session, args json.RawMessage) (string, error) {
	var a struct {
		Title       string   `json:"title"`
		Description string   `json:"description"`
		Status      string   `json:"status"`
		Type        string   `json:"type"`
		Tags        []string `json:"tags"`
		Agent       string   `json:"agent"`
		DependsOn   []string `json:"depends_on"`
		ParentID    string   `json:"parent_id"`
	}
	_ = json.Unmarshal(args, &a)
	if strings.TrimSpace(a.Title) == "" {
		return "", errors.New("title is required")
	}
	agentID, err := h.agentByName(caller.ProjectID, a.Agent)
	if err != nil {
		return "", err
	}
	id := uuid.New().String()
	if err := h.checkRelations(caller, id, a.DependsOn, a.ParentID); err != nil {
		return "", err
	}
	card := dbpkg.KanbanCard{
		ID: id, ProjectID: caller.ProjectID,
		Title: strings.TrimSpace(a.Title), Description: a.Description,
		CardType: orDefault(a.Type, "task"), Tags: a.Tags, AgentID: agentID,
		DependsOn: a.DependsOn, ParentID: a.ParentID,
	}
	if card.Tags == nil {
		card.Tags = []string{}
	}
	if card.DependsOn == nil {
		card.DependsOn = []string{}
	}
	// No status given: Todo, or In Progress when its agent is free and nothing blocks it.
	status := strings.TrimSpace(a.Status)
	if status == "" {
		status = h.defaultCreateStatus(card)
	}
	list, err := h.listByStatus(caller.ProjectID, status)
	if err != nil {
		return "", err
	}
	card.ListID = list.ID
	card.Position = h.endOf(card, list.ID)
	if err := dbpkg.InsertKanbanCard(h.db, card); err != nil {
		return "", err
	}
	card, _ = dbpkg.GetKanbanCard(h.db, id)
	out := fmt.Sprintf("Created task %s %q in %s.", card.ID, card.Title, list.Name)
	if list.Kind == dbpkg.KanbanInProgress || list.Kind == dbpkg.KanbanDone {
		card = h.afterCardSaved(card) // In Progress runs it; Done releases dependents
		out = fmt.Sprintf("Created task %s %q in %s.", card.ID, card.Title, h.listName(card.ListID))
	}
	h.notifyTaskUpdated(sessID, card)
	return out, nil
}

// taskUpdate is the task_update tool: only the fields given change. A change of
// status goes through the board's workflow. An agent moves its own task with
// task_done / task_blocked, not with this.
func (h *Handler) taskUpdate(sessID string, caller dbpkg.Session, args json.RawMessage) (string, error) {
	var a struct {
		TaskID      string    `json:"task_id"`
		Title       *string   `json:"title"`
		Description *string   `json:"description"`
		Status      *string   `json:"status"`
		Type        *string   `json:"type"`
		Tags        *[]string `json:"tags"`
		Agent       *string   `json:"agent"`
		DependsOn   *[]string `json:"depends_on"`
		ParentID    *string   `json:"parent_id"`
		Archived    *bool     `json:"archived"`
	}
	_ = json.Unmarshal(args, &a)
	card, err := h.projectTask(caller, a.TaskID)
	if err != nil {
		return "", err
	}
	var changed []string
	oldList := card.ListID

	if a.Title != nil {
		if strings.TrimSpace(*a.Title) == "" {
			return "", errors.New("title cannot be empty")
		}
		card.Title = strings.TrimSpace(*a.Title)
		changed = append(changed, "title")
	}
	if a.Description != nil {
		card.Description = *a.Description
		changed = append(changed, "description")
	}
	if a.Type != nil && strings.TrimSpace(*a.Type) != "" {
		card.CardType = strings.TrimSpace(*a.Type)
		changed = append(changed, "type")
	}
	if a.Tags != nil {
		card.Tags = *a.Tags
		if card.Tags == nil {
			card.Tags = []string{}
		}
		changed = append(changed, "tags")
	}
	if a.Agent != nil {
		agentID, err := h.agentByName(caller.ProjectID, *a.Agent)
		if err != nil {
			return "", err
		}
		card.AgentID = agentID
		changed = append(changed, "agent")
	}
	deps, parent := card.DependsOn, card.ParentID
	if a.DependsOn != nil {
		deps = *a.DependsOn
		if deps == nil {
			deps = []string{}
		}
		changed = append(changed, "depends_on")
	}
	if a.ParentID != nil {
		parent = *a.ParentID
		changed = append(changed, "parent_id")
	}
	if a.DependsOn != nil || a.ParentID != nil {
		if err := h.checkRelations(caller, card.ID, deps, parent); err != nil {
			return "", err
		}
		card.DependsOn, card.ParentID = deps, parent
	}
	if a.Archived != nil {
		card.Archived = *a.Archived
		changed = append(changed, "archived")
	}
	if a.Status != nil && strings.TrimSpace(*a.Status) != "" {
		if card.ID == caller.CardID {
			return "", errors.New("do not change the status of your own task with task_update: use task_done or task_blocked")
		}
		list, err := h.listByStatus(caller.ProjectID, *a.Status)
		if err != nil {
			return "", err
		}
		if list.ID != card.ListID {
			if reason := h.moveBlocker(dbpkg.KanbanCard{ID: card.ID, ListID: list.ID, AgentID: card.AgentID, DependsOn: card.DependsOn}, card.ListID); reason != "" {
				return "", errors.New(reason)
			}
			card.ListID = list.ID
			card.Position = h.endOf(card, list.ID)
			changed = append(changed, "status")
		}
	}
	if len(changed) == 0 {
		return "", errors.New("nothing to change: give at least one field to update")
	}
	if err := dbpkg.UpdateKanbanCard(h.db, card); err != nil {
		return "", err
	}
	card, _ = dbpkg.GetKanbanCard(h.db, card.ID)
	if card.ListID != oldList {
		card = h.afterCardSaved(card)
	}
	h.notifyTaskUpdated(sessID, card)
	return fmt.Sprintf("Updated task %s %q (%s); it is in %s.", card.ID, card.Title, strings.Join(changed, ", "), h.listName(card.ListID)), nil
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return strings.TrimSpace(s)
}

// agentBusy reports whether an agent is working: it has a task in progress or a
// session that is running or waiting for an approval.
func (h *Handler) agentBusy(projectID, agentID string) bool {
	if cards, err := dbpkg.ListKanbanCards(h.db, projectID, false); err == nil {
		for _, c := range cards {
			if c.AgentID != agentID {
				continue
			}
			if l, err := dbpkg.GetKanbanList(h.db, c.ListID); err == nil && l.Kind == dbpkg.KanbanInProgress {
				return true
			}
		}
	}
	if sessions, err := dbpkg.ListSessionsByAgent(h.db, agentID); err == nil {
		for _, s := range sessions {
			if s.Status == "running" || s.Status == "waiting_hook" {
				return true
			}
		}
	}
	return false
}

// defaultCreateStatus is where a new task goes when the caller gives no status:
// Todo, unless it has a free agent and nothing pending in front of it, in which
// case In Progress so the work starts right away.
func (h *Handler) defaultCreateStatus(card dbpkg.KanbanCard) string {
	if card.AgentID == "" || len(h.unmetDependencies(card)) > 0 || h.agentBusy(card.ProjectID, card.AgentID) {
		return dbpkg.KanbanTodo
	}
	return dbpkg.KanbanInProgress
}
