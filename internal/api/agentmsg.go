package api

import (
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	sessionpkg "claude-orchestrator/internal/session"

	"github.com/google/uuid"
)

// Agent-to-agent messaging and escalation.
//
// An agent can message a teammate (send_to_agent), list its teammates
// (agent_list) and look up a task (task_info). A message lands in the
// recipient's chat for the task it has in progress, wrapped in an envelope that
// lucode writes (so the sender cannot forge it):
//
//	#From: Alice/<agent id>
//	#Context: task <task id> "<title>"
//	#RequiresAnswer: yes|no
//
//	<message>
//
// The recipient answers by messaging back, so nobody blocks waiting. If the recipient is busy the message
// is queued and delivered when its turn ends (Manager.OnTurnEnd → DrainInbox).
//
// Every agent can have a supervisor (agents.reports_to). That supervisor is told
// on its own when a subordinate finishes, blocks or fails a task, and
// send_to_agent accepts "supervisor" as the recipient.

const (
	// A sender may post at most msgLimit messages per msgWindow: a guard against
	// two agents answering each other forever.
	msgLimit  = 10
	msgWindow = 10 * time.Minute
)

// startTurn starts a turn through the manager (overridable in tests).
func (h *Handler) startTurn(opts sessionpkg.TurnOptions) error {
	if h.startTurnFn != nil {
		return h.startTurnFn(opts)
	}
	return h.manager.StartTurn(opts)
}

// agentTurnOpts is the turn configuration for a task session on its agent.
func (h *Handler) agentTurnOpts(sess dbpkg.Session, agent dbpkg.Agent, card dbpkg.KanbanCard, proj dbpkg.Project, message string) (sessionpkg.TurnOptions, error) {
	provider, model := resolveProvider("", agent.Model)
	// Where it runs and with which AI: the agent's choice, else the project's.
	env, err := h.resolveTurnEnv(agent, proj)
	if err != nil {
		return sessionpkg.TurnOptions{}, err
	}
	runner := env.RunnerID
	return sessionpkg.TurnOptions{
		SessionID:       sess.ID,
		ProjectID:       sess.ProjectID,
		Message:         message,
		Model:           model,
		Provider:        provider,
		WorkDirs:        env.WorkDirs,
		ClaudeConfigDir: env.ConfigDir,
		RunnerID:        runner,
		AIProfileID:     env.AIProfileID,
		Resume:          sess.MessageCount > 0,
		Executor:        agent.Executor,
		SystemPrompt:    agentSystemPrompt(agent, card, h.supervisorName(agent)),
		AgentName:       agent.Name,
		Tools:           agent.Tools,
		Persistent:      agent.Persistent && agent.Executor == "claude",
		AutoMode:        agent.AutoMode && agent.Executor == "claude",
		Worktree:        agent.Worktree && agent.Executor == "claude",
		WorktreeBaseRef: agent.WorktreeBaseRef,
		LucodeTools:     agent.LucodeTools,
	}, nil
}

// supervisorName is the name of the agent this one reports to ("" if none).
func (h *Handler) supervisorName(agent dbpkg.Agent) string {
	if agent.ReportsTo == "" {
		return ""
	}
	if sup, err := dbpkg.GetAgent(h.db, agent.ReportsTo); err == nil {
		return sup.Name
	}
	return ""
}

// activeSession is the task session an agent is working on right now in a
// project: the most recently active one whose card sits in In Progress. A global
// agent can be busy in several projects, so the project narrows it down.
func (h *Handler) activeSession(agent dbpkg.Agent, projectID string) (dbpkg.Session, dbpkg.KanbanCard, bool) {
	sessions, err := dbpkg.ListSessionsByAgent(h.db, agent.ID)
	if err != nil {
		return dbpkg.Session{}, dbpkg.KanbanCard{}, false
	}
	for _, s := range sessions { // newest first
		if s.CardID == "" || (projectID != "" && s.ProjectID != projectID) {
			continue
		}
		card, err := dbpkg.GetKanbanCard(h.db, s.CardID)
		if err != nil {
			continue
		}
		if list, err := dbpkg.GetKanbanList(h.db, card.ListID); err == nil && list.Kind == dbpkg.KanbanInProgress {
			return s, card, true
		}
	}
	return dbpkg.Session{}, dbpkg.KanbanCard{}, false
}

// findTeammate resolves a recipient by name (or "supervisor") within a project.
func (h *Handler) findTeammate(projectID string, from dbpkg.Agent, to string) (dbpkg.Agent, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return dbpkg.Agent{}, errors.New("to is required: the name of the agent to message")
	}
	if strings.EqualFold(to, "supervisor") {
		if from.ReportsTo == "" {
			return dbpkg.Agent{}, errors.New("you have no supervisor")
		}
		sup, err := dbpkg.GetAgent(h.db, from.ReportsTo)
		if err != nil {
			return dbpkg.Agent{}, errors.New("your supervisor no longer exists")
		}
		return sup, nil
	}
	agents, err := dbpkg.ListAgents(h.db, projectID) // the project's agents plus the global ones
	if err != nil {
		return dbpkg.Agent{}, err
	}
	var names []string
	for _, a := range agents {
		if a.ID == from.ID {
			continue
		}
		if strings.EqualFold(a.Name, to) {
			return a, nil
		}
		names = append(names, a.Name)
	}
	if len(names) == 0 {
		return dbpkg.Agent{}, fmt.Errorf("no agent named %q: you have no teammates in this project", to)
	}
	return dbpkg.Agent{}, fmt.Errorf("no agent named %q; teammates: %s", to, strings.Join(names, ", "))
}

// formatAgentMessage is the envelope a recipient sees. lucode writes it, so
// #From cannot be forged by the sender.
func formatAgentMessage(from dbpkg.Agent, ctxCard dbpkg.KanbanCard, requiresAnswer bool, text string) string {
	answer := "no"
	if requiresAnswer {
		answer = "yes"
	}
	return fmt.Sprintf("#From: %s/%s\n#Context: task %s %q\n#RequiresAnswer: %s\n\n%s",
		from.Name, from.ID, ctxCard.ID, ctxCard.Title, answer, strings.TrimSpace(text))
}

// agentList describes the caller's teammates and what each is doing.
func (h *Handler) agentList(projectID string, from dbpkg.Agent) (string, error) {
	agents, err := dbpkg.ListAgents(h.db, projectID)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if sup := h.supervisorName(from); sup != "" {
		fmt.Fprintf(&sb, "Your supervisor: %s\n", sup)
	}
	n := 0
	for _, a := range agents {
		if a.ID == from.ID {
			continue
		}
		n++
		role := strings.TrimSpace(strings.SplitN(strings.TrimSpace(a.Personality), "\n", 2)[0])
		if len(role) > 140 {
			role = role[:140] + "…"
		}
		status := "idle (no task in progress)"
		if _, card, ok := h.activeSession(a, projectID); ok {
			status = fmt.Sprintf("working on %q (task %s)", card.Title, card.ID)
		}
		fmt.Fprintf(&sb, "- %s — %s [%s, %s]\n", a.Name, role, a.Model, status)
	}
	if n == 0 {
		sb.WriteString("You have no teammates in this project.")
	}
	return strings.TrimSpace(sb.String()), nil
}

// allowSend enforces the per-sender message rate limit.
func (h *Handler) allowSend(senderSession string) bool {
	h.msgMu.Lock()
	defer h.msgMu.Unlock()
	if h.sent == nil {
		h.sent = map[string][]time.Time{}
	}
	cutoff := time.Now().Add(-msgWindow)
	recent := h.sent[senderSession][:0]
	for _, t := range h.sent[senderSession] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}
	if len(recent) >= msgLimit {
		h.sent[senderSession] = recent
		return false
	}
	h.sent[senderSession] = append(recent, time.Now())
	return true
}

// deliverToAgent posts a message into the recipient's in-progress task chat,
// starting a turn for it (or queueing it when the recipient is mid-turn).
func (h *Handler) deliverToAgent(from dbpkg.Agent, ctxCard dbpkg.KanbanCard, to dbpkg.Agent, text string, requiresAnswer bool) (string, error) {
	sess, card, ok := h.activeSession(to, ctxCard.ProjectID)
	if !ok {
		return "", fmt.Errorf("%s has no task in progress, so it cannot receive messages right now", to.Name)
	}
	proj, err := dbpkg.GetProject(h.db, sess.ProjectID)
	if err != nil {
		return "", errors.New("project not found")
	}
	msg := formatAgentMessage(from, ctxCard, requiresAnswer, text)

	opts, err := h.agentTurnOpts(sess, to, card, proj, msg)
	if err != nil {
		return "", err
	}
	err = h.startTurn(opts)
	switch {
	case err == nil:
		return fmt.Sprintf("Message delivered to %s. Their answer will arrive as a new message.", to.Name), nil
	case errors.Is(err, sessionpkg.ErrSessionBusy):
		h.enqueue(sess.ID, msg)
		return fmt.Sprintf("%s is busy right now; the message is queued and will be delivered when it finishes its current turn.", to.Name), nil
	default:
		return "", fmt.Errorf("could not deliver the message: %w", err)
	}
}

func (h *Handler) enqueue(sessID, msg string) {
	h.msgMu.Lock()
	defer h.msgMu.Unlock()
	if h.inbox == nil {
		h.inbox = map[string][]string{}
	}
	h.inbox[sessID] = append(h.inbox[sessID], msg)
}

func (h *Handler) popInbox(sessID string) []string {
	h.msgMu.Lock()
	defer h.msgMu.Unlock()
	msgs := h.inbox[sessID]
	delete(h.inbox, sessID)
	return msgs
}

// DrainInbox delivers the messages queued for a session whose turn just ended.
// It is the Manager's OnTurnEnd hook.
func (h *Handler) DrainInbox(sessID string) {
	msgs := h.popInbox(sessID)
	if len(msgs) == 0 {
		return
	}
	requeue := func() {
		for _, m := range msgs {
			h.enqueue(sessID, m)
		}
	}
	sess, err := dbpkg.GetSession(h.db, sessID)
	if err != nil || sess.AgentID == "" || sess.CardID == "" {
		return
	}
	agent, err := dbpkg.GetAgent(h.db, sess.AgentID)
	if err != nil {
		return
	}
	card, err := dbpkg.GetKanbanCard(h.db, sess.CardID)
	if err != nil {
		return
	}
	proj, err := dbpkg.GetProject(h.db, sess.ProjectID)
	if err != nil {
		return
	}
	opts, err := h.agentTurnOpts(sess, agent, card, proj, strings.Join(msgs, "\n\n---\n\n"))
	if err == nil {
		err = h.startTurn(opts)
	}
	if errors.Is(err, sessionpkg.ErrSessionBusy) {
		requeue() // another turn started first; try again when it ends
	} else if err != nil {
		log.Printf("[agents] deliver queued messages to %s: %v", agent.Name, err)
	}
}

// notifySupervisor tells the agent this one reports to what happened. Missing
// supervisor or one with no task in progress is not an error: the event is
// still recorded on the card.
func (h *Handler) notifySupervisor(agent dbpkg.Agent, card dbpkg.KanbanCard, text string) {
	if agent.ReportsTo == "" {
		return
	}
	sup, err := dbpkg.GetAgent(h.db, agent.ReportsTo)
	if err != nil {
		return
	}
	if _, err := h.deliverToAgent(agent, card, sup, text, false); err != nil {
		log.Printf("[agents] could not notify supervisor %s: %v", sup.Name, err)
		_ = dbpkg.InsertCardComment(h.db, dbpkg.CardComment{
			ID: uuid.New().String(), CardID: card.ID, Author: "lucode", Kind: "comment",
			Body: fmt.Sprintf("Could not notify supervisor %s: %v", sup.Name, err),
		})
	}
}

// HandleTurnFailed is the Manager's OnTurnFailed hook. A turn that cannot run
// (today: a session that cannot be resumed) is recorded on the task, which moves
// to Blocked, and the supervising agent is told. There is no automatic retry.
func (h *Handler) HandleTurnFailed(sessID, code, message string) {
	sess, err := dbpkg.GetSession(h.db, sessID)
	if err != nil || sess.CardID == "" {
		return
	}
	card, err := dbpkg.GetKanbanCard(h.db, sess.CardID)
	if err != nil {
		return
	}
	author := "lucode"
	agent, agentErr := dbpkg.GetAgent(h.db, sess.AgentID)
	if agentErr == nil {
		author = agent.Name
	}
	_ = dbpkg.InsertCardComment(h.db, dbpkg.CardComment{
		ID: uuid.New().String(), CardID: card.ID, SessionID: sessID, Author: author, Kind: "error", Body: message,
	})
	moved, err := h.moveCardToKind(card, dbpkg.KanbanBlocked)
	if err != nil {
		moved = card
	}
	h.notifyTaskUpdated(sessID, moved)
	if agentErr == nil {
		h.notifySupervisor(agent, moved, fmt.Sprintf("Task %q failed and was moved to Blocked: %s", moved.Title, message))
	}
}

// closeLives stops the live claude processes of every session of an agent, so a
// changed configuration (model, tools, prompt, persistence) takes effect.
func (h *Handler) closeLives(agentID string) {
	if h.manager == nil {
		return
	}
	sessions, err := dbpkg.ListSessionsByAgent(h.db, agentID)
	if err != nil {
		return
	}
	for _, s := range sessions {
		h.manager.CloseLive(s.ID)
	}
}
