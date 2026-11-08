package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	dbpkg "claude-orchestrator/internal/db"
	sessionpkg "claude-orchestrator/internal/session"
	"claude-orchestrator/internal/taskmcp"

	"github.com/google/uuid"
)

// The orchestrator is the project's main chat (the panel on the right of the UI).
// It is a normal session of kind "orchestrator" that always runs on a persistent
// `claude -p` process (stream-json), on the runner and preconfigured AI the user
// picks under the input, and it has the lucode MCP tools to manage the board and
// the agents of its project. It belongs to no task and is no agent, so the three
// tools that report on one's own task (task_comment, task_done, task_blocked) do
// not apply to it.

// orchestratorTools are the lucode tools the orchestrator gets: every one that
// does not need a task of its own.
func orchestratorTools() []string {
	var out []string
	for _, n := range taskmcp.Names() {
		switch n {
		case taskmcp.ToolComment, taskmcp.ToolDone, taskmcp.ToolBlocked:
			continue
		}
		out = append(out, n)
	}
	return append(append(out, taskmcp.BrowserNames()...), taskmcp.FlowNames()...)
}

// orchestratorCaller is the identity the agent-management tools see: an agent of
// the project that is not stored (its empty id never matches a real agent), with
// every lucode tool and no limit on the built-in ones it may hand out.
func orchestratorCaller(projectID string) dbpkg.Agent {
	return dbpkg.Agent{Name: "orchestrator", ProjectID: projectID, Executor: "claude"}
}

const orchestratorPrompt = `# You are the lucode Supervisor
You are the Supervisor: the main chat of this project in lucode. The user talks to you to plan and run the work; you do not do the project's work yourself unless it is small: you organise it on the board and hand it to the project's agents.

Your tools (in Claude Code they appear as mcp__lucode__<name>):
- project_info(): where you are: this project's name, work folders, runner, AI, board counts and agents. Call it first when you need to know the project's context.
- task_list(status?, limit?): see the board. Check it before creating a task so you do not duplicate one.
- task_info(task_id, fields?): look up one task; ask for the fewest fields that answer your question.
- task_create(title, description?, status?, type?, tags?, agent?, depends_on?, parent_id?): create a task. It goes to Todo by default, or straight to In Progress (the agent starts) when you assign an agent that is free and nothing blocks it; status=backlog just parks it. Write a description that lets whoever takes it work without asking you.
- task_update(task_id, ...fields): change a task; only the fields you pass change. Moving it to in_progress starts its agent.
- get_task_image(name, task_id): see a picture attached to a task.
- agent_list(): the agents of the project and whether each is working.
- agent_create / agent_update / agent_delete: manage the project's agents. Use agent_list first. Give each a clear role in its personality.
- flow_list, flow_get, flow_catalog, flow_create, flow_update: prepare the project's automations with the user, who sees your changes live in the Automate tab. You only write drafts: the user publishes them. Read a flow again before changing it.
- send_to_agent(to, message, task_id, requires_answer?): talk to an agent that has a task in progress; task_id (the task the message is about) is required. The answer comes back later as a message.
- git_diff(mode, path?, sha?, base?, head?, runner_id?, repo?, worktree?): read a diff of a repository of the project. It only reads, and it works even if the repository is on another runner than you.

The user can send you references from the Git tab: a block starting with "Git reference:" names a file, its lines and where they come from (a commit, two branches, the working tree...), with the selected lines when there are any. Those lines are the point of the reference: answer about them. When you need more around them, call git_diff with the arguments the reference gives.

Rules:
- Ask the user before doing something large or hard to undo (deleting agents, starting many tasks at once).
- Tell the user briefly what you did and what is running; do not wait for agents to finish inside a turn.
- Some tools ask the user for approval. If one is denied, do not retry it; adapt or explain what you need.`

// orchEnvs remembers, per orchestrator session, the runner and AI its live process
// was started with, so a change in the selects restarts the process.
var (
	orchEnvMu  sync.Mutex
	orchEnvs   = map[string]string{}
	orchModels = map[string]string{}
)

// getOrchestrator returns the project's orchestrator session, creating it on first use.
func (h *Handler) getOrchestrator(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	if _, err := dbpkg.GetProject(h.db, pid); err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	sess, err := dbpkg.GetOrchestratorSession(h.db, pid)
	if errors.Is(err, sql.ErrNoRows) {
		sess, err = h.createOrchestrator(pid)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, sess)
}

func (h *Handler) createOrchestrator(pid string) (dbpkg.Session, error) {
	sess := dbpkg.Session{
		ID: uuid.New().String(), ProjectID: pid, Name: "Orchestrator", Model: "sonnet",
		Status: string(sessionpkg.StatusIdle), Kind: dbpkg.SessionKindOrchestrator,
	}
	if err := dbpkg.InsertSession(h.db, sess); err != nil {
		return sess, err
	}
	return dbpkg.GetSession(h.db, sess.ID)
}

// newOrchestrator starts a fresh orchestrator conversation: the old one stops (its live
// claude process is closed) and a new session, with an empty context, takes its place.
func (h *Handler) newOrchestrator(w http.ResponseWriter, r *http.Request) {
	pid := r.PathValue("pid")
	if _, err := dbpkg.GetProject(h.db, pid); err != nil {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	if old, err := dbpkg.GetOrchestratorSession(h.db, pid); err == nil {
		if h.manager != nil {
			h.manager.CancelTurn(old.ID)
			h.manager.CloseLive(old.ID)
		}
		orchEnvMu.Lock()
		delete(orchEnvs, old.ID)
		delete(orchModels, old.ID)
		orchEnvMu.Unlock()
		if h.plugins != nil {
			h.plugins.SessionClosed(pid, old.ID)
		}
	}
	sess, err := h.createOrchestrator(pid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, sess)
}

// orchestratorTurnOpts builds the turn of an orchestrator session: always the
// claude CLI with a persistent process, on the chosen runner and AI ("" or "local"
// runner = this instance).
func (h *Handler) orchestratorTurnOpts(sess dbpkg.Session, proj dbpkg.Project, message, model, runnerID, aiProfileID, effort, thinking string) (sessionpkg.TurnOptions, error) {
	if model == "" {
		model = sess.Model
	}
	if pluginOpts, handled, err := h.pluginTurnOpts(sess, message, model); handled {
		return pluginOpts, err
	}
	if runnerID == "" {
		runnerID = "local" // explicit: do not inherit the project's runner
	}
	if h.hubOnly && runnerID == "local" {
		runnerID = "" // a hub has no claude of its own: the chat runs where the project does
	}
	env, err := h.resolveTurnEnv(dbpkg.Agent{RunnerID: runnerID, AIProfileID: aiProfileID}, proj)
	if err != nil {
		return sessionpkg.TurnOptions{}, err
	}
	_, model = resolveProvider("", model)

	resume := sess.MessageCount > 0
	key := env.RunnerID + "|" + env.AIProfileID
	orchEnvMu.Lock()
	prev, seen := orchEnvs[sess.ID]
	orchEnvs[sess.ID] = key
	orchEnvMu.Unlock()
	// The persistent process is started with --model: another model needs a new process,
	// but the conversation (same runner and AI) is resumed.
	orchEnvMu.Lock()
	prevModel, modelSeen := orchModels[sess.ID]
	orchModels[sess.ID] = model
	orchEnvMu.Unlock()
	if modelSeen && prevModel != model && !(seen && prev != key) {
		h.manager.CloseLive(sess.ID)
	}
	if seen && prev != key {
		// The conversation of claude lives with the runner/AI that ran it: restart the
		// process there (without --resume, its transcript is not on the new one).
		h.manager.CloseLive(sess.ID)
		resume = false
	}
	return sessionpkg.TurnOptions{
		SessionID:       sess.ID,
		ProjectID:       sess.ProjectID,
		Message:         message,
		Model:           model,
		WorkDirs:        env.WorkDirs,
		ClaudeConfigDir: env.ConfigDir,
		RunnerID:        env.RunnerID,
		AIProfileID:     env.AIProfileID,
		Resume:          resume,
		Effort:          effort,
		Thinking:        thinking,
		Executor:        "claude",
		SystemPrompt:    orchestratorPrompt + browserPrompt,
		AgentName:       "orchestrator",
		Persistent:      true,
		LucodeTools:     orchestratorTools(),
	}, nil
}

// handleOrchestratorTool runs a lucode tool called by an orchestrator session.
func (h *Handler) handleOrchestratorTool(sessID string, sess dbpkg.Session, tool string, args json.RawMessage) (string, error) {
	toolName := strings.TrimPrefix(tool, "mcp__lucode__")
	if taskmcp.IsBrowserTool(toolName) {
		return h.handleBrowserTool(toolName, args)
	}
	if taskmcp.IsFlowTool(toolName) {
		return h.handleFlowTool(sess.ProjectID, "Supervisor", toolName, args)
	}
	var a struct {
		To             string   `json:"to"`
		Message        string   `json:"message"`
		TaskID         string   `json:"task_id"`
		RequiresAnswer bool     `json:"requires_answer"`
		Fields         []string `json:"fields"`
		Name           string   `json:"name"`
	}
	_ = json.Unmarshal(args, &a)
	caller := orchestratorCaller(sess.ProjectID)

	switch toolName {
	case taskmcp.ToolProjectInfo:
		return h.projectInfo(sess)
	case taskmcp.ToolGitDiff:
		return h.gitDiffTool(sess.ProjectID, args)
	case taskmcp.ToolTaskList:
		return h.taskList(sess, args)
	case taskmcp.ToolTaskCreate:
		return h.taskCreate(sessID, sess, args)
	case taskmcp.ToolTaskUpdate:
		return h.taskUpdate(sessID, sess, args)
	case taskmcp.ToolTaskInfo:
		if strings.TrimSpace(a.TaskID) == "" {
			return "", errors.New("task_id is required")
		}
		return h.taskInfo(sess, a.TaskID, a.Fields)
	case taskmcp.ToolGetTaskImage:
		if strings.TrimSpace(a.TaskID) == "" {
			return "", errors.New("task_id is required")
		}
		return h.taskImage(sess, a.TaskID, a.Name)
	case taskmcp.ToolAgentList:
		return h.agentList(sess.ProjectID, caller)
	case taskmcp.ToolAgentCreate:
		return h.agentCreate(caller, args)
	case taskmcp.ToolAgentUpdate:
		return h.agentUpdate(caller, args)
	case taskmcp.ToolAgentDelete:
		return h.agentDelete(caller, args)
	case taskmcp.ToolSendToAgent:
		if strings.TrimSpace(a.Message) == "" {
			return "", errors.New("message is required")
		}
		id := strings.TrimSpace(a.TaskID)
		if id == "" {
			return "", errors.New("task_id is required: the task the message is about")
		}
		card, err := dbpkg.GetKanbanCard(h.db, id)
		if err != nil || card.ProjectID != sess.ProjectID {
			return "", fmt.Errorf("task %q not found in this project", id)
		}
		to, err := h.findTeammate(sess.ProjectID, caller, a.To)
		if err != nil {
			return "", err
		}
		if !h.allowSend(sessID) {
			return "", fmt.Errorf("message limit reached (%d per %s)", msgLimit, msgWindow)
		}
		return h.deliverToAgent(caller, card, to, a.Message, a.RequiresAnswer)
	}
	return "", fmt.Errorf("the %s tool does not apply to the orchestrator", toolName)
}
