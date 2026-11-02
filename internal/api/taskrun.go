package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	dbpkg "claude-orchestrator/internal/db"
	sessionpkg "claude-orchestrator/internal/session"
	"claude-orchestrator/internal/taskmcp"

	"github.com/google/uuid"
)

// Task execution. A kanban card is a task; moving it into the "In Progress"
// list starts its assigned agent. Cards whose dependencies are not done yet are
// parked in "Blocked" and released automatically when the last dependency
// reaches "Done".

// ensureCardSession returns the task's chat session, creating it on first use.
// Reassigning the card moves the same conversation to the new agent.
func (h *Handler) ensureCardSession(card dbpkg.KanbanCard, agent dbpkg.Agent) (dbpkg.Session, bool, error) {
	if sess, err := dbpkg.GetSessionByCard(h.db, card.ID); err == nil {
		if sess.AgentID != agent.ID {
			if err := dbpkg.UpdateSessionAgent(h.db, sess.ID, agent.ID, agent.Model); err != nil {
				return sess, false, err
			}
			sess.AgentID, sess.Model = agent.ID, agent.Model
		}
		return sess, false, nil
	}
	sess := dbpkg.Session{
		ID:        uuid.New().String(),
		ProjectID: card.ProjectID,
		Name:      card.Title,
		Model:     agent.Model,
		Status:    string(sessionpkg.StatusIdle),
		CardID:    card.ID,
		AgentID:   agent.ID,
	}
	if err := dbpkg.InsertSession(h.db, sess); err != nil {
		return sess, false, err
	}
	return sess, true, nil
}

// afterCardSaved runs the workflow side effects of a card that was just saved
// into list newListID (only called when the list actually changed). It returns
// the card as it should be shown, since a move can be redirected to Blocked.
func (h *Handler) afterCardSaved(card dbpkg.KanbanCard) dbpkg.KanbanCard {
	list, err := dbpkg.GetKanbanList(h.db, card.ListID)
	if err != nil {
		return card
	}
	switch list.Kind {
	case dbpkg.KanbanInProgress:
		return h.startTask(card)
	case dbpkg.KanbanDone:
		h.releaseDependents(card)
	}
	return card
}

// startTask runs a card that entered "In Progress". With unmet dependencies it
// is moved to "Blocked" instead.
func (h *Handler) startTask(card dbpkg.KanbanCard) dbpkg.KanbanCard {
	if unmet := h.unmetDependencies(card); len(unmet) > 0 {
		log.Printf("[task] %q blocked by: %s", card.Title, strings.Join(unmet, ", "))
		if moved, err := h.moveCardToKind(card, dbpkg.KanbanBlocked); err == nil {
			return moved
		}
		return card
	}
	if card.AgentID == "" {
		log.Printf("[task] %q is in progress but has no agent — nothing to run", card.Title)
		return card
	}
	if err := h.runTask(card); err != nil {
		log.Printf("[task] run %q: %v", card.Title, err)
	}
	return card
}

// unmetDependencies returns the titles of dependencies that are not in a Done list.
func (h *Handler) unmetDependencies(card dbpkg.KanbanCard) []string {
	var unmet []string
	for _, depID := range card.DependsOn {
		dep, err := dbpkg.GetKanbanCard(h.db, depID)
		if err != nil {
			continue // deleted dependency no longer blocks
		}
		list, err := dbpkg.GetKanbanList(h.db, dep.ListID)
		if err != nil || list.Kind != dbpkg.KanbanDone {
			unmet = append(unmet, dep.Title)
		}
	}
	return unmet
}

// releaseDependents starts blocked cards whose dependencies are all done now.
func (h *Handler) releaseDependents(done dbpkg.KanbanCard) {
	cards, err := dbpkg.ListKanbanCards(h.db, done.ProjectID, false)
	if err != nil {
		return
	}
	for _, c := range cards {
		if c.ID == done.ID || !contains(c.DependsOn, done.ID) {
			continue
		}
		list, err := dbpkg.GetKanbanList(h.db, c.ListID)
		if err != nil || list.Kind != dbpkg.KanbanBlocked {
			continue
		}
		if len(h.unmetDependencies(c)) > 0 {
			continue
		}
		moved, err := h.moveCardToKind(c, dbpkg.KanbanInProgress)
		if err != nil {
			log.Printf("[task] release %q: %v", c.Title, err)
			continue
		}
		h.startTask(moved)
	}
}

func contains(ids []string, id string) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// moveCardToKind moves a card to the end of the project's list with that role.
func (h *Handler) moveCardToKind(card dbpkg.KanbanCard, kind string) (dbpkg.KanbanCard, error) {
	target, err := dbpkg.FindKanbanListByKind(h.db, card.ProjectID, kind)
	if err != nil {
		return card, fmt.Errorf("no %q list: %w", kind, err)
	}
	pos := 0
	if all, err := dbpkg.ListKanbanCards(h.db, card.ProjectID, false); err == nil {
		for _, c := range all {
			if c.ListID == target.ID && c.Position >= pos {
				pos = c.Position + 1
			}
		}
	}
	card.ListID, card.Position = target.ID, pos
	if err := dbpkg.UpdateKanbanCard(h.db, card); err != nil {
		return card, err
	}
	return dbpkg.GetKanbanCard(h.db, card.ID)
}

// runTask starts a turn for the card on its agent. The first run sends the
// task itself; a re-run (card moved back to In Progress) asks to continue.
func (h *Handler) runTask(card dbpkg.KanbanCard) error {
	agent, err := dbpkg.GetAgent(h.db, card.AgentID)
	if err != nil {
		return fmt.Errorf("assigned agent not found: %w", err)
	}
	proj, err := dbpkg.GetProject(h.db, card.ProjectID)
	if err != nil {
		return fmt.Errorf("project not found: %w", err)
	}
	sess, _, err := h.ensureCardSession(card, agent)
	if err != nil {
		return err
	}

	message := h.taskPrompt(card)
	if sess.MessageCount > 0 {
		message = "This task was moved back to In Progress. Review the current state of the work and continue.\n\n" + message
	}

	opts := h.agentTurnOpts(sess, agent, card, proj, message)
	if err := h.startTurn(opts); err != nil {
		if errors.Is(err, sessionpkg.ErrSessionBusy) {
			return nil // already running
		}
		return err
	}
	log.Printf("[task] started %q on agent %q (%s)", card.Title, agent.Name, agent.Executor)
	return nil
}

// taskPrompt is the first message an agent receives for a task.
func (h *Handler) taskPrompt(card dbpkg.KanbanCard) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Task: %s\nTask id: %s\n", card.Title, card.ID)
	if card.CardType != "" {
		fmt.Fprintf(&sb, "Type: %s\n", card.CardType)
	}
	if len(card.Tags) > 0 {
		fmt.Fprintf(&sb, "Tags: %s\n", strings.Join(card.Tags, ", "))
	}
	if card.ParentID != "" {
		if parent, err := dbpkg.GetKanbanCard(h.db, card.ParentID); err == nil {
			fmt.Fprintf(&sb, "Part of epic: %s\n", parent.Title)
		}
	}
	if len(card.DependsOn) > 0 {
		var titles []string
		for _, id := range card.DependsOn {
			if dep, err := dbpkg.GetKanbanCard(h.db, id); err == nil {
				titles = append(titles, dep.Title)
			}
		}
		if len(titles) > 0 {
			fmt.Fprintf(&sb, "Completed dependencies: %s\n", strings.Join(titles, ", "))
		}
	}
	if strings.TrimSpace(card.Description) != "" {
		fmt.Fprintf(&sb, "\n%s\n", card.Description)
	}
	return sb.String()
}

// agentSystemPrompt is what gets appended to the agent's system prompt: its own
// personality followed by how to work with lucode (reporting on the task).
func agentSystemPrompt(agent dbpkg.Agent, card dbpkg.KanbanCard, supervisor string) string {
	var sb strings.Builder
	if p := strings.TrimSpace(agent.Personality); p != "" {
		sb.WriteString(p)
		sb.WriteString("\n\n")
	}
	fmt.Fprintf(&sb, `# Working inside lucode
You are %q, an agent in lucode working on the task %q (id %s) from this project's board. The user follows your progress on that board, and the task only changes state when you tell lucode.

Your task is described in your first message. Work only on it. Report through the lucode task tools (in Claude Code they appear as mcp__lucode__task_comment, mcp__lucode__task_done and mcp__lucode__task_blocked):
- task_comment(text): post a short progress note when you reach something worth telling the user. It does not change the status.
- task_done(summary): call it ONLY when the work is finished and you verified it. The summary says what you did and where the result is. It moves the task to Done and unblocks the tasks that depend on it.
- task_blocked(reason): call it when you cannot continue without the user (missing information, a decision, access, or a tool they denied). It moves the task to Blocked.

Rules:
- Never say the task is finished without calling task_done; do not call it for unfinished work.
- After task_done or task_blocked, stop: end your turn with one short closing message.
- Some tools ask the user for approval. If one is denied, do not retry it; adapt, or call task_blocked and explain what you need.

Working with other agents:
- agent_list(): see your teammates, what each one does and whether it is working on something (with the id of that task).
- send_to_agent(to, message, task_id?, requires_answer?): talk to a teammate. lucode delivers it into the task they are working on and adds the envelope. Set requires_answer=true only when you really need a reply; set task_id when the message is about a task other than your own. The reply comes back later as a message to you, so do not wait for it: keep working or end your turn. A teammate with no task in progress cannot receive messages.
- task_info(task_id?, fields?): look up a task of this project (your own if you give no id). Ask only for the fields you need: title, description, status, type, tags, agent, dependencies, parent, dates, comments.
- Messages from other agents start with three header lines: "#From: <name>/<id>", "#Context: task <id> <title>" (use task_info on that id if you need the details) and "#RequiresAnswer: yes|no". If it is yes, answer with send_to_agent to that agent once you have what they asked for; if it is no, do not reply unless you have something they need.
- Keep messages short and specific; do not use them for chit-chat.`, agent.Name, card.Title, card.ID)
	if supervisor != "" {
		fmt.Fprintf(&sb, "\n- Your supervisor is %s: they are told automatically when you finish, get blocked or fail, so do not message them just to report that. Use send_to_agent with to=\"supervisor\" for questions or decisions you need from them.", supervisor)
	}
	return sb.String()
}

// HandleTaskTool runs a task tool an agent called (task_comment, task_done or
// task_blocked) for the task its session belongs to. It is wired to the NATS
// bus; the returned text is what the agent sees as the tool result.
func (h *Handler) HandleTaskTool(sessID, tool string, args json.RawMessage) (string, error) {
	sess, err := dbpkg.GetSession(h.db, sessID)
	if err != nil {
		return "", errors.New("session not found")
	}
	if sess.CardID == "" {
		return "", errors.New("this session is not linked to a task")
	}
	card, err := dbpkg.GetKanbanCard(h.db, sess.CardID)
	if err != nil {
		return "", errors.New("the task no longer exists")
	}
	var a struct {
		Text    string `json:"text"`
		Summary string `json:"summary"`
		Reason  string `json:"reason"`
		To             string   `json:"to"`
		Message        string   `json:"message"`
		TaskID         string   `json:"task_id"`
		RequiresAnswer bool     `json:"requires_answer"`
		Fields         []string `json:"fields"`
	}
	_ = json.Unmarshal(args, &a)

	author := "agent"
	agent, agentErr := dbpkg.GetAgent(h.db, sess.AgentID)
	if agentErr == nil {
		author = agent.Name
	}
	comment := func(kind, body string) error {
		return dbpkg.InsertCardComment(h.db, dbpkg.CardComment{
			ID: uuid.New().String(), CardID: card.ID, SessionID: sessID, Author: author, Kind: kind, Body: body,
		})
	}

	switch strings.TrimPrefix(tool, "mcp__lucode__") {
	case taskmcp.ToolComment:
		if strings.TrimSpace(a.Text) == "" {
			return "", errors.New("text is required")
		}
		if err := comment("comment", a.Text); err != nil {
			return "", err
		}
		h.notifyTaskUpdated(sessID, card)
		return "Comment posted on the task.", nil

	case taskmcp.ToolDone:
		if strings.TrimSpace(a.Summary) == "" {
			return "", errors.New("summary is required: say what you did and where the result is")
		}
		if err := comment("done", a.Summary); err != nil {
			return "", err
		}
		moved, err := h.moveCardToKind(card, dbpkg.KanbanDone)
		if err != nil {
			return "", fmt.Errorf("could not move the task to Done: %w", err)
		}
		h.releaseDependents(moved)
		h.notifyTaskUpdated(sessID, moved)
		if agentErr == nil {
			h.notifySupervisor(agent, moved, fmt.Sprintf("Task %q is done: %s", moved.Title, a.Summary))
		}
		return "Task marked as done. Dependent tasks were released. End your turn now with a short closing message.", nil

	case taskmcp.ToolAgentList:
		if agentErr != nil {
			return "", errNoAgent
		}
		return h.agentList(agent)

	case taskmcp.ToolTaskInfo:
		return h.taskInfo(sess, a.TaskID, a.Fields)

	case taskmcp.ToolSendToAgent:
		if agentErr != nil {
			return "", errNoAgent
		}
		if strings.TrimSpace(a.Message) == "" {
			return "", errors.New("message is required")
		}
		to, err := h.findTeammate(agent, a.To)
		if err != nil {
			return "", err
		}
		// the task the message is about: another one of this project, or the sender's own
		ctxCard := card
		if id := strings.TrimSpace(a.TaskID); id != "" && id != card.ID {
			c, err := dbpkg.GetKanbanCard(h.db, id)
			if err != nil || c.ProjectID != sess.ProjectID {
				return "", fmt.Errorf("task %q not found in this project", id)
			}
			ctxCard = c
		}
		if !h.allowSend(sessID) {
			return "", fmt.Errorf("message limit reached (%d per %s): finish your own work instead of messaging", msgLimit, msgWindow)
		}
		return h.deliverToAgent(agent, ctxCard, to, a.Message, a.RequiresAnswer)

	case taskmcp.ToolBlocked:
		if strings.TrimSpace(a.Reason) == "" {
			return "", errors.New("reason is required: say what blocks you and what you need")
		}
		if err := comment("blocked", a.Reason); err != nil {
			return "", err
		}
		moved, err := h.moveCardToKind(card, dbpkg.KanbanBlocked)
		if err != nil {
			return "", fmt.Errorf("could not move the task to Blocked: %w", err)
		}
		h.notifyTaskUpdated(sessID, moved)
		if agentErr == nil {
			h.notifySupervisor(agent, moved, fmt.Sprintf("Task %q is blocked: %s", moved.Title, a.Reason))
		}
		return "Task marked as blocked. The user was notified. End your turn now with a short closing message.", nil
	}
	return "", fmt.Errorf("unknown task tool %q", tool)
}

// notifyTaskUpdated tells connected browsers the board changed so they refresh.
func (h *Handler) notifyTaskUpdated(sessID string, card dbpkg.KanbanCard) {
	if h.bus == nil {
		return
	}
	h.bus.Broadcast(sessID, map[string]any{
		"type":       "task_updated",
		"session_id": sessID,
		"project_id": card.ProjectID,
		"card_id":    card.ID,
	})
}

// listCardComments returns the progress notes agents posted on a task.
func (h *Handler) listCardComments(w http.ResponseWriter, r *http.Request) {
	comments, err := dbpkg.ListCardComments(h.db, r.PathValue("id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, comments)
}
