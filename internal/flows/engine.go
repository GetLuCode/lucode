package flows

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	dbpkg "claude-orchestrator/internal/db"
)

// Limits that keep an automation from running away.
const (
	MaxDepth       = 8   // flows triggered by flows, in a chain
	MaxStepsPerRun = 200 // steps of one run, loops included
	MaxAgentTurns  = 10  // agent turns and AI evaluations of one run: these are what cost tokens
)

// aiCallTimeout bounds one AI evaluation, retries included. The step's own timer is a
// little longer, so the call normally reports its timeout itself.
const (
	aiCallTimeout = 5 * time.Minute
	aiStepTimeout = aiCallTimeout + 30*time.Second
)

// gitStepTimeout covers the git calls, which have their own shorter limits on the way.
const gitStepTimeout = 5 * time.Minute

// pushStepTimeout covers one push, whose calls to the push services have a shorter limit.
const pushStepTimeout = 2 * time.Minute

// PushMessage is a notification for the devices. The run says what clicking it opens, and
// Seq identifies it on the device: the same step sent twice shows once.
type PushMessage struct {
	Title     string
	Body      string
	ProjectID string
	FlowID    string
	RunID     string
	Seq       int64
}

// PushResult counts what happened to the devices: Removed ones no longer exist.
type PushResult struct {
	Sent    int
	Failed  int
	Removed int
}

// GitState is what Git status found. State is the output to take.
type GitState struct {
	State         string
	Branch        string
	Base          string
	Ahead         int
	Files         []string
	ConflictFiles []string
	WorktreePath  string
}

// MergeOptions are the choices of a Merge node.
type MergeOptions struct {
	Strategy       string
	Message        string
	DeleteWorktree bool
	DeleteBranch   bool
	MoveToDone     bool
}

// AIRequest asks a model to choose one of the outcomes for a text.
type AIRequest struct {
	Model       string
	Effort      string
	Instruction string
	Input       string
	Outcomes    []string
}

// AIResult is what the model answered. The engine checks Outcome against the node's list.
type AIResult struct {
	Outcome      string
	Reason       string
	Model        string
	InputTokens  int64
	OutputTokens int64
}

// ErrNotWaiting means a decision came for a step that no longer waits for one.
var ErrNotWaiting = errors.New("this approval is no longer waiting for a decision")

// portAll is the result of a node whose every output runs (Parallel).
const portAll = "*"

// turnEndGrace holds turn.ended back for a moment: the hub reports the end and the failure
// of a turn from separate goroutines, and a waiting step must see the failure first.
const defaultTurnEndGrace = 2 * time.Second

// Host is what the engine needs from the hub. Everything that changes the board or
// starts a turn goes through it, under the same rules as a user or an agent.
type Host interface {
	// CardScope returns the card as expressions see it.
	CardScope(cardID string) (map[string]any, error)
	// MoveCard moves a card. A non-empty rejected says why a board rule refused it.
	MoveCard(cardID, kind, listID string, cause dbpkg.EventCause) (rejected string, err error)
	Comment(cardID, text string) error
	AddTag(cardID, tag string) error
	CreateTask(projectID, title, description, status, agent string, cause dbpkg.EventCause) (cardID string, err error)
	// RunAgent starts a turn on the card's session and returns that session. The turn runs
	// on a runner; the engine waits for its turn.ended or turn.failed event.
	RunAgent(cardID, agent, prompt string, cause dbpkg.EventCause) (sessionID string, err error)
	// AIEvaluate calls the model. The engine runs it away from its own goroutine.
	AIEvaluate(ctx context.Context, req AIRequest) (AIResult, error)
	// GitStatus reads the card's branch on its runner.
	GitStatus(cardID string) (GitState, error)
	// GitMerge merges the card's branch on its runner and returns the output to take.
	GitMerge(ctx context.Context, cardID string, o MergeOptions, cause dbpkg.EventCause) (string, map[string]any, error)
	// GitMergeFinished does the hub's half of a merge that is known to have happened.
	GitMergeFinished(cardID string, o MergeOptions, cause dbpkg.EventCause) error
	NotifySupervisor(cardID, text string) error
	// SendPush notifies the subscribed devices. An error means no device could be reached.
	SendPush(ctx context.Context, msg PushMessage) (PushResult, error)
	// RunChanged tells the browsers a run changed.
	RunChanged(run dbpkg.FlowRun)
}

// Engine runs flows. One goroutine does all the work, which makes the order of events
// and steps deterministic and keeps writes to SQLite simple.
type Engine struct {
	db           *sql.DB
	host         Host
	wake         chan struct{}
	TurnEndGrace time.Duration
	now          func() time.Time
	ctx          context.Context
	calls        sync.WaitGroup // background calls in flight
	merges       sync.Mutex     // one merge at a time: they fight over the checkout

	mu     sync.Mutex
	graphs map[string]Graph // flow id + version -> graph; versions never change

	ToolTimeout time.Duration // how long DecideTool waits for the flows
	tools       toolWaits

	// EventTap sees every event as it is handled (plugins react to some). It must not block.
	EventTap func(dbpkg.FlowEvent)
}

// NewEngine creates an engine.
func NewEngine(db *sql.DB, host Host) *Engine {
	return &Engine{
		db: db, host: host, wake: make(chan struct{}, 1), TurnEndGrace: defaultTurnEndGrace,
		now: time.Now, graphs: map[string]Graph{}, ctx: context.Background(),
		ToolTimeout: defaultToolTimeout, tools: toolWaits{m: map[string]*toolWait{}},
	}
}

// Notify asks the engine to look for work now rather than at its next tick.
func (e *Engine) Notify() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// Start recovers what a previous stop interrupted and runs until ctx ends.
func (e *Engine) Start(ctx context.Context) {
	e.ctx = ctx
	e.Recover()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		prune := time.NewTicker(6 * time.Hour)
		defer prune.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-e.wake:
			case <-ticker.C:
			case <-prune.C:
				if err := dbpkg.PruneFlowEvents(e.db, 7*24*time.Hour); err != nil {
					log.Printf("[flows] prune: %v", err)
				}
				if err := dbpkg.PruneToolFlowRuns(e.db, 7*24*time.Hour); err != nil {
					log.Printf("[flows] prune tool runs: %v", err)
				}
			}
			e.Drain()
		}
	}()
}

// Drain works until there is nothing left to do right now. The bound only matters if
// the database keeps failing: it stops a retry from spinning between ticks.
func (e *Engine) Drain() {
	for i := 0; i < 1000 && e.tick(); i++ {
	}
}

func (e *Engine) tick() bool {
	worked := e.dispatchEvents()
	if e.fireSchedules() {
		worked = true
	}
	if e.wakeDue() {
		worked = true
	}
	if e.runReady() {
		worked = true
	}
	return worked
}

// Recover handles the steps a stopped hub left running. A repeatable one runs again. One
// that may have already acted (it may have posted the comment, or started the turn) is not
// repeated blindly: it fails and says why, so a person can decide.
func (e *Engine) Recover() {
	// A run deciding a tool has nobody to answer now: the request died with the old hub.
	if open, err := dbpkg.OpenToolFlowRuns(e.db); err != nil {
		log.Printf("[flows] recover tool runs: %v", err)
	} else {
		for _, id := range open {
			if _, err := dbpkg.FinishFlowRun(e.db, id, dbpkg.RunCancelled, "the hub stopped while this tool request was being decided"); err != nil {
				log.Printf("[flows] recover tool run %s: %v", id, err)
			}
		}
	}
	steps, err := dbpkg.InterruptedFlowSteps(e.db)
	if err != nil {
		log.Printf("[flows] recover: %v", err)
		return
	}
	for _, s := range steps {
		run, _, node, err := e.stepContext(s)
		if err != nil {
			continue
		}
		if def, ok := NodeTypeOf(node.Type); ok && def.Repeatable {
			_ = dbpkg.RetryFlowStep(e.db, s.Seq)
			continue
		}
		e.failStep(run, s, "the hub stopped while this step was running: it may or may not have finished")
	}
	// The answer of an evaluation that was in flight is gone. Evaluating changes nothing, so
	// it can simply be asked again. Approvals need nothing: they wait in the database.
	waiting, err := dbpkg.WaitingFlowStepsWithPrefix(e.db, aiWaitPrefix)
	if err != nil {
		log.Printf("[flows] recover evaluations: %v", err)
		return
	}
	for _, s := range waiting {
		run, _, node, err := e.stepContext(s)
		if err != nil || run.Status != dbpkg.RunRunning {
			continue
		}
		scope, err := e.scope(run)
		if err == nil {
			err = e.startEvaluation(run, node, s, scope)
		}
		if err != nil {
			e.failStep(run, s, err.Error())
		}
	}
	gitSteps, err := dbpkg.WaitingFlowStepsWithPrefix(e.db, gitWaitPrefix)
	if err != nil {
		log.Printf("[flows] recover git steps: %v", err)
		return
	}
	for _, s := range gitSteps {
		run, _, node, err := e.stepContext(s)
		if err != nil || run.Status != dbpkg.RunRunning {
			continue
		}
		if node.Type == TypeGitStatus {
			e.startGitStatus(run, s) // reading twice does no harm
		} else {
			e.startMergeCheck(run, node, s)
		}
	}
	// A device that already got this one just replaces it, so sending again is safe. The text is
	// the one the step rendered, even if the card has changed since.
	pushSteps, err := dbpkg.WaitingFlowStepsWithPrefix(e.db, pushWaitPrefix)
	if err != nil {
		log.Printf("[flows] recover push steps: %v", err)
		return
	}
	for _, s := range pushSteps {
		run, _, _, err := e.stepContext(s)
		if err != nil || run.Status != dbpkg.RunRunning {
			continue
		}
		var text struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		_ = json.Unmarshal(s.Output, &text)
		e.startPush(run, s, text.Title, text.Body)
	}
	if err := e.recoverPluginSteps(); err != nil {
		log.Printf("[flows] recover plugin steps: %v", err)
	}
}

func (e *Engine) startPush(run dbpkg.FlowRun, s dbpkg.FlowStep, title, body string) {
	msg := PushMessage{Title: title, Body: body, ProjectID: run.ProjectID, FlowID: run.FlowID, RunID: run.ID, Seq: s.Seq}
	e.startCall(run, pushWaitKey(s.Seq), func(ctx context.Context) (string, map[string]any, error) {
		res, err := e.host.SendPush(ctx, msg)
		if err != nil {
			return "", nil, err
		}
		return PortNext, map[string]any{"sent": res.Sent, "failed": res.Failed, "removed": res.Removed}, nil
	})
}

// startCall runs slow work away from the engine's goroutine. Its result comes back as an
// event, so it is handled in order like everything else.
func (e *Engine) startCall(run dbpkg.FlowRun, key string, call func(ctx context.Context) (string, map[string]any, error)) {
	e.calls.Add(1)
	go func() {
		defer e.calls.Done()
		port, output, err := call(e.ctx)
		payload := map[string]any{"wait_key": key}
		for k, v := range output {
			payload[k] = v
		}
		if err != nil {
			if e.ctx.Err() != nil {
				return // the hub is stopping: Recover picks it up
			}
			port = PortFailed
			payload["error"] = err.Error()
		}
		payload["port"] = port
		if err := dbpkg.RecordFlowEvent(e.db, run.ProjectID, dbpkg.EventCallDone, run.CardID, "", payload); err != nil {
			log.Printf("[flows] record call: %v", err)
			return
		}
		e.Notify()
	}()
}

func (e *Engine) startGitStatus(run dbpkg.FlowRun, s dbpkg.FlowStep) {
	cardID := run.CardID
	e.startCall(run, gitWaitKey(s.Seq), func(context.Context) (string, map[string]any, error) {
		st, err := e.host.GitStatus(cardID)
		if err != nil {
			return "", nil, err
		}
		return st.State, map[string]any{
			"state": st.State, "branch": st.Branch, "base": st.Base, "ahead": st.Ahead,
			"files": st.Files, "conflict_files": st.ConflictFiles, "worktree_path": st.WorktreePath,
		}, nil
	})
}

func mergeOptionsOf(node Node, scope Scope) (MergeOptions, error) {
	msg, err := RenderTemplate(node.str("message"), scope)
	if err != nil {
		return MergeOptions{}, err
	}
	return MergeOptions{
		Strategy: node.str("strategy"), Message: msg,
		DeleteWorktree: node.boolean("delete_worktree", true),
		DeleteBranch:   node.boolean("delete_branch", true),
		MoveToDone:     node.boolean("move_to_done", true),
	}, nil
}

// startMergeCheck picks up a merge the hub stopped in the middle of. It is not repeated
// blindly: the runner may still be on it. If the branch turns out merged, the hub finishes its
// half; otherwise the run fails and says so.
func (e *Engine) startMergeCheck(run dbpkg.FlowRun, node Node, s dbpkg.FlowStep) {
	scope, err := e.scope(run)
	var opts MergeOptions
	if err == nil {
		opts, err = mergeOptionsOf(node, scope)
	}
	if err != nil {
		e.failStep(run, s, err.Error())
		return
	}
	cause := e.cause(run)
	e.startCall(run, gitWaitKey(s.Seq), func(context.Context) (string, map[string]any, error) {
		st, err := e.host.GitStatus(run.CardID)
		if err != nil {
			return "", nil, fmt.Errorf("the hub stopped during this merge and cannot tell if it finished: %w", err)
		}
		if st.State != PortNothing {
			return "", nil, errors.New("the hub stopped during this merge and the branch is not merged: nothing was retried, check the repository and run it again")
		}
		if err := e.host.GitMergeFinished(run.CardID, opts, cause); err != nil {
			return "", nil, err
		}
		return PortMerged, map[string]any{"recovered": true}, nil
	})
}

// WaitForEvaluations blocks until no AI evaluation is in flight (tests).
func (e *Engine) WaitForEvaluations() { e.calls.Wait() }

const (
	aiWaitPrefix       = "ai:"
	gitWaitPrefix      = "git:"
	pushWaitPrefix     = "push:"
	approvalWaitPrefix = "approval:"
)

func aiWaitKey(seq int64) string       { return fmt.Sprintf("%s%d", aiWaitPrefix, seq) }
func approvalWaitKey(seq int64) string { return fmt.Sprintf("%s%d", approvalWaitPrefix, seq) }
func gitWaitKey(seq int64) string      { return fmt.Sprintf("%s%d", gitWaitPrefix, seq) }
func pushWaitKey(seq int64) string     { return fmt.Sprintf("%s%d", pushWaitPrefix, seq) }

// startEvaluation calls the model in the background. The answer comes back as an event, so it
// is handled in order on the engine's goroutine, like everything else.
func (e *Engine) startEvaluation(run dbpkg.FlowRun, node Node, s dbpkg.FlowStep, scope Scope) error {
	instruction, err := RenderTemplate(node.str("instruction"), scope)
	if err != nil {
		return err
	}
	input, err := RenderTemplate(node.str("input"), scope)
	if err != nil {
		return err
	}
	var outcomes []string
	for _, f := range []string{"outcome1", "outcome2", "outcome3"} {
		if v := node.str(f); v != "" {
			outcomes = append(outcomes, v)
		}
	}
	req := AIRequest{Model: node.str("model"), Effort: node.str("effort"), Instruction: instruction, Input: input, Outcomes: outcomes}
	key := aiWaitKey(s.Seq)
	e.calls.Add(1)
	go func() {
		defer e.calls.Done()
		ctx, cancel := context.WithTimeout(e.ctx, aiCallTimeout)
		defer cancel()
		res, err := e.host.AIEvaluate(ctx, req)
		payload := map[string]any{"wait_key": key}
		if err != nil {
			if e.ctx.Err() != nil {
				return // the hub is stopping: Recover asks again after the restart
			}
			payload["error"] = err.Error()
		} else {
			payload["outcome"] = res.Outcome
			payload["reason"] = res.Reason
			payload["model"] = res.Model
			payload["input_tokens"] = res.InputTokens
			payload["output_tokens"] = res.OutputTokens
		}
		if err := dbpkg.RecordFlowEvent(e.db, run.ProjectID, dbpkg.EventAIEvaluated, run.CardID, "", payload); err != nil {
			log.Printf("[flows] record evaluation: %v", err)
			return
		}
		e.Notify()
	}()
	return nil
}

// Decide answers a Human approval. The decision travels as an event, so it reaches the step
// on the engine's goroutine; a second click finds nothing left to wake.
func (e *Engine) Decide(seq int64, decision, note string) error {
	if decision != PortApproved && decision != PortRejected {
		return errors.New("the decision must be approved or rejected")
	}
	s, err := dbpkg.GetFlowStep(e.db, seq)
	if err != nil || s.Status != dbpkg.StepWaiting || s.WaitKey != approvalWaitKey(seq) {
		return ErrNotWaiting
	}
	run, err := dbpkg.GetFlowRun(e.db, s.RunID)
	if err != nil || run.Status != dbpkg.RunRunning {
		return ErrNotWaiting
	}
	payload := map[string]any{"wait_key": s.WaitKey, "decision": decision, "note": note}
	if err := dbpkg.RecordFlowEvent(e.db, run.ProjectID, dbpkg.EventApprovalDecided, run.CardID, "", payload); err != nil {
		return err
	}
	e.Notify()
	return nil
}

// ---- events ----

func (e *Engine) dispatchEvents() bool {
	events, err := dbpkg.PendingFlowEvents(e.db, 100)
	if err != nil {
		log.Printf("[flows] read events: %v", err)
		return false
	}
	worked := false
	now := e.now()
	for _, ev := range events {
		if ev.Type == dbpkg.EventTurnEnded && now.Sub(ev.CreatedAt) < e.TurnEndGrace {
			continue
		}
		e.resolveWaits(ev)
		e.startRuns(ev)
		if e.EventTap != nil {
			e.EventTap(ev)
		}
		if err := dbpkg.MarkFlowEventDispatched(e.db, ev.ID); err != nil {
			log.Printf("[flows] mark event %d: %v", ev.ID, err)
			return worked
		}
		worked = true
	}
	return worked
}

func sessionWaitKey(id string) string { return "session:" + id }
func eventWaitKey(typ, cardID string) string {
	return "event:" + typ + ":" + cardID
}

// resolveWaits wakes the steps waiting for this event.
func (e *Engine) resolveWaits(ev dbpkg.FlowEvent) {
	if key, _ := ev.Payload["wait_key"].(string); key != "" {
		steps, _ := dbpkg.WaitingFlowSteps(e.db, key)
		for _, s := range steps {
			_, _, node, err := e.stepContext(s)
			if err != nil {
				continue
			}
			extra := map[string]any{}
			for k, v := range ev.Payload {
				if k != "wait_key" {
					extra[k] = v
				}
			}
			port := fmt.Sprint(ev.Payload["decision"])
			switch ev.Type {
			case dbpkg.EventAIEvaluated:
				port = outcomePort(node, extra)
			case dbpkg.EventCallDone:
				port = fmt.Sprint(ev.Payload["port"])
				delete(extra, "port")
			}
			e.resumeStep(s, port, extra)
		}
		return
	}
	if ev.SessionID != "" && (ev.Type == dbpkg.EventTurnEnded || ev.Type == dbpkg.EventTurnFailed) {
		steps, _ := dbpkg.WaitingFlowSteps(e.db, sessionWaitKey(ev.SessionID))
		for _, s := range steps {
			port := PortNext
			if ev.Type == dbpkg.EventTurnFailed {
				port = PortFailed
			}
			e.resumeStep(s, port, ev.Payload)
		}
	}
	if ev.CardID == "" {
		return
	}
	steps, _ := dbpkg.WaitingFlowSteps(e.db, eventWaitKey(ev.Type, ev.CardID))
	for _, s := range steps {
		_, _, node, err := e.stepContext(s)
		if err != nil {
			continue
		}
		if want := node.str("to_kind"); want != "" && want != "any" && want != fmt.Sprint(ev.Payload["to_kind"]) {
			continue
		}
		e.resumeStep(s, PortEvent, ev.Payload)
	}
}

// resumeStep finishes a waiting step through a port, keeping what it stored when it parked.
func (e *Engine) resumeStep(s dbpkg.FlowStep, port string, extra map[string]any) {
	run, g, _, err := e.stepContext(s)
	if err != nil || run.Status != dbpkg.RunRunning {
		return
	}
	output := map[string]any{}
	_ = json.Unmarshal(s.Output, &output)
	for k, v := range extra {
		output[k] = v
	}
	e.finishStep(run, g, s, port, output)
}

// startRuns starts the flows of the project that have a trigger for this event.
func (e *Engine) startRuns(ev dbpkg.FlowEvent) {
	if ev.Type == dbpkg.EventAIEvaluated || ev.Type == dbpkg.EventApprovalDecided || ev.Type == dbpkg.EventCallDone {
		return
	}
	if ev.Type == dbpkg.EventToolRequested {
		e.startToolRuns(ev)
		return
	}
	if dbpkg.FlowsPaused(e.db, ev.ProjectID) {
		return
	}
	flows, err := dbpkg.ActiveFlows(e.db, ev.ProjectID)
	if err != nil {
		log.Printf("[flows] list flows: %v", err)
		return
	}
	for _, f := range flows {
		if ev.FlowID == f.ID {
			continue // a flow does not react to its own moves
		}
		g, err := e.graph(f.ID, f.PublishedVersion)
		if err != nil {
			log.Printf("[flows] flow %s: %v", f.ID, err)
			continue
		}
		var starts []string
		for _, n := range g.Nodes {
			if triggerMatches(n, ev) {
				starts = append(starts, n.ID)
			}
		}
		if len(starts) == 0 {
			continue
		}
		event, _ := json.Marshal(eventScope(ev))
		run := dbpkg.FlowRun{
			FlowID: f.ID, Version: f.PublishedVersion, ProjectID: ev.ProjectID, CardID: ev.CardID,
			EventID: ev.ID, Event: event, Depth: ev.Depth, Status: dbpkg.RunRunning,
		}
		if ev.Depth >= MaxDepth {
			run.Status = dbpkg.RunFailed
			run.Error = fmt.Sprintf("not started: %d flows in a row led to this event, which looks like a loop", ev.Depth)
			run.FinishedAt = dbpkg.FlowNow()
			starts = nil
		} else if ev.CardID != "" && dbpkg.HasActiveFlowRun(e.db, f.ID, ev.CardID) {
			run.Status = dbpkg.RunQueued // one run per flow and card, in order
		}
		created, err := dbpkg.CreateFlowRun(e.db, run, starts)
		if err != nil {
			if !errors.Is(err, dbpkg.ErrFlowRunExists) {
				log.Printf("[flows] start flow %s: %v", f.ID, err)
			}
			continue
		}
		e.host.RunChanged(created)
	}
}

func eventScope(ev dbpkg.FlowEvent) map[string]any {
	out := map[string]any{}
	for k, v := range ev.Payload {
		out[k] = v
	}
	out["type"] = ev.Type
	out["source"] = ev.Source
	out["session_id"] = ev.SessionID
	return out
}

func triggerMatches(n Node, ev dbpkg.FlowEvent) bool {
	kindOK := func(field, payloadKey string) bool {
		want := n.str(field)
		return want == "" || want == "any" || want == fmt.Sprint(ev.Payload[payloadKey])
	}
	switch n.Type {
	case TypeCardMoved:
		if ev.Type != dbpkg.EventCardMoved {
			return false
		}
		if list := n.str("to_list"); list != "" {
			if list != fmt.Sprint(ev.Payload["to_list"]) {
				return false
			}
		} else if !kindOK("to_kind", "to_kind") {
			return false
		}
		return kindOK("from_kind", "from_kind")
	case TypeCardCreated:
		return ev.Type == dbpkg.EventCardCreated && kindOK("to_kind", "to_kind")
	case TypeTaskDone:
		return ev.Type == dbpkg.EventTaskDone
	case TypeTurnEnded:
		return ev.Type == dbpkg.EventTurnEnded
	case TypeTurnFailed:
		return ev.Type == dbpkg.EventTurnFailed
	case TypePluginEvent:
		return ev.Type == dbpkg.EventPluginEvent && n.str("plugin") == fmt.Sprint(ev.Payload["plugin"]) && n.str("event") == fmt.Sprint(ev.Payload["event"])
	}
	return false
}

// StartManual starts a published flow from its Manual triggers.
func (e *Engine) StartManual(f dbpkg.Flow, cardID string) (dbpkg.FlowRun, error) {
	if f.PublishedVersion == 0 {
		return dbpkg.FlowRun{}, errors.New("publish the flow first")
	}
	g, err := e.graph(f.ID, f.PublishedVersion)
	if err != nil {
		return dbpkg.FlowRun{}, err
	}
	var starts []string
	for _, n := range g.Nodes {
		if n.Type == TypeManual {
			starts = append(starts, n.ID)
		}
	}
	if len(starts) == 0 {
		return dbpkg.FlowRun{}, errors.New("this flow has no Manual trigger")
	}
	event, _ := json.Marshal(map[string]any{"type": "manual", "source": "user"})
	run := dbpkg.FlowRun{
		FlowID: f.ID, Version: f.PublishedVersion, ProjectID: f.ProjectID, CardID: cardID,
		Event: event, Status: dbpkg.RunRunning,
	}
	if cardID != "" && dbpkg.HasActiveFlowRun(e.db, f.ID, cardID) {
		run.Status = dbpkg.RunQueued
	}
	created, err := dbpkg.CreateFlowRun(e.db, run, starts)
	if err != nil {
		return created, err
	}
	e.host.RunChanged(created)
	e.Notify()
	return created, nil
}

// Cancel stops a run. A turn already running on a runner is not interrupted: the run just
// stops reacting to it.
func (e *Engine) Cancel(runID string) error {
	closed, err := dbpkg.FinishFlowRun(e.db, runID, dbpkg.RunCancelled, "cancelled by the user")
	if err != nil {
		return err
	}
	if closed {
		e.runClosed(runID)
	}
	return nil
}

// ---- schedules and timers ----

func (e *Engine) fireSchedules() bool {
	flows, err := dbpkg.AllActiveFlows(e.db)
	if err != nil {
		return false
	}
	worked := false
	now := e.now().UTC()
	for _, f := range flows {
		if dbpkg.FlowsPaused(e.db, f.ProjectID) {
			continue
		}
		g, err := e.graph(f.ID, f.PublishedVersion)
		if err != nil {
			continue
		}
		for _, n := range g.Nodes {
			if n.Type != TypeSchedule {
				continue
			}
			last := dbpkg.FlowScheduleLastFired(e.db, f.ID, n.ID)
			if last == "" { // the first interval counts from now, not from the epoch
				_ = dbpkg.SetFlowScheduleLastFired(e.db, f.ID, n.ID, now.Format(time.RFC3339Nano))
				continue
			}
			t, err := time.Parse(time.RFC3339Nano, last)
			if err != nil || now.Sub(t) < time.Duration(n.num("minutes", 60)*float64(time.Minute)) {
				continue
			}
			_ = dbpkg.SetFlowScheduleLastFired(e.db, f.ID, n.ID, now.Format(time.RFC3339Nano))
			event, _ := json.Marshal(map[string]any{"type": "schedule", "source": "system"})
			created, err := dbpkg.CreateFlowRun(e.db, dbpkg.FlowRun{
				FlowID: f.ID, Version: f.PublishedVersion, ProjectID: f.ProjectID, Event: event, Status: dbpkg.RunRunning,
			}, []string{n.ID})
			if err != nil {
				log.Printf("[flows] schedule of flow %s: %v", f.ID, err)
				continue
			}
			e.host.RunChanged(created)
			worked = true
		}
	}
	return worked
}

func (e *Engine) wakeDue() bool {
	steps, err := dbpkg.DueFlowSteps(e.db, e.now().UTC().Format("2006-01-02T15:04:05.000Z"))
	if err != nil {
		return false
	}
	for _, s := range steps {
		_, _, node, err := e.stepContext(s)
		if err != nil {
			continue
		}
		port := PortNext
		var extra map[string]any
		switch node.Type {
		case TypeWaitEvent, TypeApproval:
			port = PortTimeout
		case TypeAIEvaluate:
			port = PortFailed
			extra = map[string]any{"error": "the API did not answer in time"}
		case TypeGitStatus:
			port = PortFailed
			extra = map[string]any{"error": "the runner did not answer in time"}
		case TypeMerge:
			port = PortFailed
			extra = map[string]any{"error": "the runner did not answer in time: the merge may or may not have happened, check the repository"}
		case TypePushNotification:
			port = PortFailed
			extra = map[string]any{"error": "the push services did not answer in time"}
		default:
			if IsPluginNode(node.Type) {
				port = PortFailed
				extra = map[string]any{"error": "the plugin did not answer in time"}
			}
		}
		e.resumeStep(s, port, extra)
	}
	return len(steps) > 0
}

// ---- steps ----

func (e *Engine) graph(flowID string, version int) (Graph, error) {
	key := fmt.Sprintf("%s@%d", flowID, version)
	e.mu.Lock()
	g, ok := e.graphs[key]
	e.mu.Unlock()
	if ok {
		return g, nil
	}
	raw, err := dbpkg.GetFlowVersion(e.db, flowID, version)
	if err != nil {
		return g, fmt.Errorf("version %d: %w", version, err)
	}
	if g, err = ParseGraph(raw); err != nil {
		return g, err
	}
	e.mu.Lock()
	e.graphs[key] = g
	e.mu.Unlock()
	return g, nil
}

func (e *Engine) stepContext(s dbpkg.FlowStep) (dbpkg.FlowRun, Graph, Node, error) {
	run, err := dbpkg.GetFlowRun(e.db, s.RunID)
	if err != nil {
		return run, Graph{}, Node{}, err
	}
	g, err := e.graph(run.FlowID, run.Version)
	if err != nil {
		return run, g, Node{}, err
	}
	n, ok := g.node(s.NodeID)
	if !ok {
		return run, g, n, fmt.Errorf("node %s is not in version %d", s.NodeID, run.Version)
	}
	return run, g, n, nil
}

func (e *Engine) runReady() bool {
	steps, err := dbpkg.ReadyFlowSteps(e.db, 50)
	if err != nil {
		log.Printf("[flows] read steps: %v", err)
		return false
	}
	worked := false
	for _, s := range steps {
		run, g, node, err := e.stepContext(s)
		if err != nil {
			log.Printf("[flows] step %d: %v", s.Seq, err)
			e.failStep(run, s, err.Error())
			worked = true
			continue
		}
		if run.Status != dbpkg.RunRunning || dbpkg.FlowsPaused(e.db, run.ProjectID) {
			continue // a pause holds the steps until it is lifted
		}
		if dbpkg.CountFlowSteps(e.db, run.ID, "") > MaxStepsPerRun {
			e.failStep(run, s, fmt.Sprintf("the run reached the limit of %d steps", MaxStepsPerRun))
			worked = true
			continue
		}
		worked = true
		if err := dbpkg.StartFlowStep(e.db, s.Seq); err != nil {
			log.Printf("[flows] start step: %v", err)
			continue
		}
		e.host.RunChanged(run)
		e.execute(run, g, node, s)
	}
	return worked
}

// result is what a node did: continue through a port, or park.
type result struct {
	port   string
	output map[string]any
	waitOn string
	wakeAt time.Time
	finish string // non-empty ends the run with this status (the Stop node)
	parked bool   // the node already parked its own step
	reason string
}

func (e *Engine) execute(run dbpkg.FlowRun, g Graph, node Node, s dbpkg.FlowStep) {
	res, err := e.executeNode(run, g, node, s)
	if err != nil {
		e.failStep(run, s, err.Error())
		return
	}
	if res.parked {
		return
	}
	if res.finish != "" {
		output, _ := json.Marshal(res.output)
		if err := dbpkg.FinishFlowStep(e.db, s, dbpkg.StepSucceeded, "", output, "", nil); err != nil {
			log.Printf("[flows] finish step: %v", err)
		}
		e.closeRun(run.ID, res.finish, res.reason)
		return
	}
	if res.waitOn != "" || !res.wakeAt.IsZero() {
		wake := ""
		if !res.wakeAt.IsZero() {
			wake = res.wakeAt.UTC().Format("2006-01-02T15:04:05.000Z")
		}
		output, _ := json.Marshal(res.output)
		if err := dbpkg.WaitFlowStep(e.db, s.Seq, res.waitOn, wake, output); err != nil {
			e.failStep(run, s, err.Error())
			return
		}
		e.host.RunChanged(run)
		return
	}
	e.finishStep(run, g, s, res.port, res.output)
}

func (e *Engine) finishStep(run dbpkg.FlowRun, g Graph, s dbpkg.FlowStep, port string, output map[string]any) {
	body, _ := json.Marshal(output)
	var next []string
	switch port {
	case "": // nothing follows (a Join still waiting for other paths)
	case portAll:
		if node, ok := g.node(s.NodeID); ok {
			if def, ok := NodeTypeOf(node.Type); ok {
				for _, p := range def.Ports {
					next = append(next, g.next(s.NodeID, p)...)
				}
			}
		}
	default:
		next = g.next(s.NodeID, port)
	}
	// A failure nobody handles is the run's failure, not a quiet success.
	if port == PortFailed && len(next) == 0 {
		msg := fmt.Sprint(output["error"])
		if output["error"] == nil {
			msg = fmt.Sprint(output["message"])
		}
		if output["error"] == nil && output["message"] == nil {
			msg = "the step failed and its \"failed\" output is not connected"
		}
		e.failStep(run, s, msg)
		return
	}
	if err := dbpkg.FinishFlowStep(e.db, s, dbpkg.StepSucceeded, port, body, "", next); err != nil {
		log.Printf("[flows] finish step: %v", err)
		return
	}
	if !dbpkg.FlowRunHasOpenSteps(e.db, run.ID) {
		e.closeRun(run.ID, dbpkg.RunSucceeded, "")
		return
	}
	e.host.RunChanged(run)
}

func (e *Engine) failStep(run dbpkg.FlowRun, s dbpkg.FlowStep, msg string) {
	if err := dbpkg.FinishFlowStep(e.db, s, dbpkg.StepFailed, "", nil, msg, nil); err != nil {
		log.Printf("[flows] fail step: %v", err)
	}
	e.closeRun(s.RunID, dbpkg.RunFailed, msg)
}

func (e *Engine) closeRun(runID, status, msg string) {
	closed, err := dbpkg.FinishFlowRun(e.db, runID, status, msg)
	if err != nil {
		log.Printf("[flows] close run: %v", err)
		return
	}
	if closed {
		e.runClosed(runID)
	}
}

// runClosed tells the browsers and lets the next run queued on the same card start.
func (e *Engine) runClosed(runID string) {
	run, err := dbpkg.GetFlowRun(e.db, runID)
	if err != nil {
		return
	}
	e.host.RunChanged(run)
	e.toolRunEnded(run)
	if run.CardID == "" || dbpkg.HasActiveFlowRun(e.db, run.FlowID, run.CardID) {
		return
	}
	next, err := dbpkg.NextQueuedFlowRun(e.db, run.FlowID, run.CardID)
	if err != nil {
		return
	}
	if err := dbpkg.SetFlowRunRunning(e.db, next.ID); err == nil {
		next.Status = dbpkg.RunRunning
		e.host.RunChanged(next)
		e.Notify()
	}
}

// scope builds the data expressions can read, with the card as it is right now.
func (e *Engine) scope(run dbpkg.FlowRun) (Scope, error) {
	event := map[string]any{}
	_ = json.Unmarshal(run.Event, &event)
	card := map[string]any{}
	if run.CardID != "" {
		c, err := e.host.CardScope(run.CardID)
		if err != nil {
			return nil, fmt.Errorf("the run's card: %w", err)
		}
		card = c
	}
	steps, err := dbpkg.ListFlowSteps(e.db, run.ID)
	if err != nil {
		return nil, err
	}
	nodes := map[string]any{}
	for _, s := range steps { // oldest first: the newest output of a node wins
		if s.Status != dbpkg.StepSucceeded {
			continue
		}
		output := map[string]any{}
		_ = json.Unmarshal(s.Output, &output)
		nodes[s.NodeID] = output
	}
	return Scope{
		"card": card, "event": event, "nodes": nodes,
		"run": map[string]any{"id": run.ID, "flow_id": run.FlowID, "version": int64(run.Version), "depth": int64(run.Depth)},
	}, nil
}

func (e *Engine) cause(run dbpkg.FlowRun) dbpkg.EventCause {
	return dbpkg.EventCause{Source: "flow", FlowID: run.FlowID, RunID: run.ID, Depth: run.Depth + 1}
}

func needCard(run dbpkg.FlowRun, title string) error {
	if run.CardID == "" {
		return fmt.Errorf("%s needs a card, and this run has none", title)
	}
	return nil
}

func (e *Engine) executeNode(run dbpkg.FlowRun, g Graph, node Node, s dbpkg.FlowStep) (result, error) {
	def, _ := NodeTypeOf(node.Type)
	if def.Category == CategoryTrigger {
		return result{port: PortNext, output: map[string]any{}}, nil
	}
	scope, err := e.scope(run)
	if err != nil {
		return result{}, err
	}
	render := func(field string) (string, error) { return RenderTemplate(node.str(field), scope) }

	if IsPluginNode(node.Type) {
		return e.startPluginNode(run, g, node, s, scope)
	}

	switch node.Type {
	case TypeIf:
		ok, err := EvalBool(node.str("condition"), scope)
		if err != nil {
			return result{}, err
		}
		port := PortFalse
		if ok {
			port = PortTrue
		}
		return result{port: port, output: map[string]any{"result": ok}}, nil

	case TypeSwitch:
		v, err := EvalString(node.str("value"), scope)
		if err != nil {
			return result{}, err
		}
		for _, c := range []string{"case1", "case2", "case3"} {
			if want := node.str(c); want != "" && want == v {
				return result{port: c, output: map[string]any{"value": v}}, nil
			}
		}
		return result{port: PortDefault, output: map[string]any{"value": v}}, nil

	case TypeParallel:
		return result{port: portAll, output: map[string]any{}}, nil

	case TypeJoin:
		steps, err := dbpkg.ListFlowSteps(e.db, run.ID)
		if err != nil {
			return result{}, err
		}
		arrived := map[string]bool{}
		for _, st := range steps {
			if st.NodeID == node.ID && st.Status != dbpkg.StepCancelled {
				arrived[st.FromNode] = true
			}
		}
		for _, from := range g.incoming(node.ID) {
			if !arrived[from] {
				return result{port: "", output: map[string]any{"waiting_for": from}}, nil // the last path to arrive continues
			}
		}
		return result{port: PortNext, output: map[string]any{}}, nil

	case TypeWaitEvent:
		if err := needCard(run, def.Title); err != nil {
			return result{}, err
		}
		return result{
			waitOn: eventWaitKey(node.str("event"), run.CardID),
			wakeAt: e.now().Add(time.Duration(node.num("timeout_minutes", 60) * float64(time.Minute))),
			output: map[string]any{},
		}, nil

	case TypeDelay:
		return result{wakeAt: e.now().Add(time.Duration(node.num("seconds", 60) * float64(time.Second))), output: map[string]any{}}, nil

	case TypeRetry:
		// this activation is already counted, so max=3 goes back on activations 1, 2 and 3
		times := dbpkg.CountFlowSteps(e.db, run.ID, node.ID)
		if float64(times) <= node.num("max", 3) {
			return result{port: PortAgain, output: map[string]any{"attempt": times}}, nil
		}
		return result{port: PortExhausted, output: map[string]any{"attempt": times}}, nil

	case TypeStop:
		msg, err := render("message")
		if err != nil {
			return result{}, err
		}
		if node.str("result") == RunFailedName {
			return result{finish: dbpkg.RunFailed, reason: msg, output: map[string]any{"message": msg}}, nil
		}
		return result{finish: dbpkg.RunSucceeded, reason: msg, output: map[string]any{"message": msg}}, nil

	case TypeMatchTool:
		port, err := matchToolStep(run, node)
		if err != nil {
			return result{}, err
		}
		return result{port: port, output: map[string]any{"matched": port == PortMatch}}, nil

	case TypeToolDecision:
		reason, err := render("reason")
		if err != nil {
			return result{}, err
		}
		decision := node.str("decision")
		if err := e.decideToolStep(run, decision, reason); err != nil {
			return result{}, err
		}
		return result{finish: dbpkg.RunSucceeded, output: map[string]any{"decision": decision, "reason": reason}}, nil

	case TypeApproval:
		question, err := render("question")
		if err != nil {
			return result{}, err
		}
		res := result{waitOn: approvalWaitKey(s.Seq), output: map[string]any{"question": question}}
		if minutes := node.num("timeout_minutes", 0); minutes > 0 {
			res.wakeAt = e.now().Add(time.Duration(minutes * float64(time.Minute)))
		}
		return res, nil

	case TypeGitStatus, TypeMerge:
		if err := needCard(run, def.Title); err != nil {
			return result{}, err
		}
		var opts MergeOptions
		if node.Type == TypeMerge {
			if opts, err = mergeOptionsOf(node, scope); err != nil {
				return result{}, err
			}
		}
		wake := e.now().Add(gitStepTimeout).UTC().Format("2006-01-02T15:04:05.000Z")
		if err := dbpkg.WaitFlowStep(e.db, s.Seq, gitWaitKey(s.Seq), wake, json.RawMessage(`{}`)); err != nil {
			return result{}, err
		}
		if node.Type == TypeGitStatus {
			e.startGitStatus(run, s)
		} else {
			cause := e.cause(run)
			e.startCall(run, gitWaitKey(s.Seq), func(ctx context.Context) (string, map[string]any, error) {
				e.merges.Lock()
				defer e.merges.Unlock()
				return e.host.GitMerge(ctx, run.CardID, opts, cause)
			})
		}
		e.host.RunChanged(run)
		return result{parked: true}, nil

	case TypeNotifySupervisor:
		if err := needCard(run, def.Title); err != nil {
			return result{}, err
		}
		text, err := render("text")
		if err != nil {
			return result{}, err
		}
		if err := e.host.NotifySupervisor(run.CardID, text); err != nil {
			return result{}, err
		}
		return result{port: PortNext, output: map[string]any{"text": text}}, nil

	case TypePushNotification:
		title, err := render("title")
		if err != nil {
			return result{}, err
		}
		body, err := render("body")
		if err != nil {
			return result{}, err
		}
		title, body = strings.TrimSpace(title), strings.TrimSpace(body)
		if title == "" || body == "" {
			return result{}, errors.New("Push notification: the title or the message came out empty")
		}
		// parked with its text before the call starts: a restart sends this same text again
		text, _ := json.Marshal(map[string]any{"title": title, "body": body})
		wake := e.now().Add(pushStepTimeout).UTC().Format("2006-01-02T15:04:05.000Z")
		if err := dbpkg.WaitFlowStep(e.db, s.Seq, pushWaitKey(s.Seq), wake, text); err != nil {
			return result{}, err
		}
		e.startPush(run, s, title, body)
		e.host.RunChanged(run)
		return result{parked: true}, nil

	case TypeAIEvaluate:
		if turns := e.agentTurns(run, g); turns > MaxAgentTurns {
			return result{}, fmt.Errorf("the run reached the limit of %d agent or AI turns", MaxAgentTurns)
		}
		// parked before the call starts: the answer can only be handled after this step commits
		wake := e.now().Add(aiStepTimeout).UTC().Format("2006-01-02T15:04:05.000Z")
		if err := dbpkg.WaitFlowStep(e.db, s.Seq, aiWaitKey(s.Seq), wake, json.RawMessage(`{}`)); err != nil {
			return result{}, err
		}
		if err := e.startEvaluation(run, node, s, scope); err != nil {
			return result{}, err
		}
		e.host.RunChanged(run)
		return result{parked: true}, nil

	case TypeRunAgent:
		if err := needCard(run, def.Title); err != nil {
			return result{}, err
		}
		if turns := e.agentTurns(run, g); turns > MaxAgentTurns {
			return result{}, fmt.Errorf("the run reached the limit of %d agent or AI turns", MaxAgentTurns)
		}
		prompt, err := render("prompt")
		if err != nil {
			return result{}, err
		}
		sess, err := e.host.RunAgent(run.CardID, node.str("agent"), prompt, e.cause(run))
		if err != nil {
			return result{}, err
		}
		return result{waitOn: sessionWaitKey(sess), output: map[string]any{"session_id": sess}}, nil

	case TypeMoveCard:
		if err := needCard(run, def.Title); err != nil {
			return result{}, err
		}
		if node.str("to_kind") == "" && node.str("to_list") == "" {
			return result{}, errors.New("Move card: choose where to")
		}
		rejected, err := e.host.MoveCard(run.CardID, node.str("to_kind"), node.str("to_list"), e.cause(run))
		if err != nil {
			return result{}, err
		}
		if rejected != "" {
			return result{port: PortRejected, output: map[string]any{"reason": rejected}}, nil
		}
		return result{port: PortNext, output: map[string]any{}}, nil

	case TypeComment:
		if err := needCard(run, def.Title); err != nil {
			return result{}, err
		}
		text, err := render("text")
		if err != nil {
			return result{}, err
		}
		if err := e.host.Comment(run.CardID, text); err != nil {
			return result{}, err
		}
		return result{port: PortNext, output: map[string]any{"text": text}}, nil

	case TypeAddTag:
		if err := needCard(run, def.Title); err != nil {
			return result{}, err
		}
		tag, err := render("tag")
		if err != nil {
			return result{}, err
		}
		if tag = strings.TrimSpace(tag); tag == "" {
			return result{}, errors.New("Add tag: the tag came out empty")
		}
		if err := e.host.AddTag(run.CardID, tag); err != nil {
			return result{}, err
		}
		return result{port: PortNext, output: map[string]any{"tag": tag}}, nil

	case TypeCreateTask:
		title, err := render("title")
		if err != nil {
			return result{}, err
		}
		desc, err := render("description")
		if err != nil {
			return result{}, err
		}
		id, err := e.host.CreateTask(run.ProjectID, title, desc, node.str("status"), node.str("agent"), e.cause(run))
		if err != nil {
			return result{}, err
		}
		return result{port: PortNext, output: map[string]any{"card_id": id}}, nil
	}
	return result{}, fmt.Errorf("unknown node type %q", node.Type)
}

// outcomePort maps the model's answer to the node's output. Only an answer the node lists can
// choose a path: anything else takes "failed", and never a default path.
func outcomePort(node Node, output map[string]any) string {
	if output["error"] != nil {
		return PortFailed
	}
	answer := fmt.Sprint(output["outcome"])
	for i, f := range []string{"outcome1", "outcome2", "outcome3"} {
		if v := node.str(f); v != "" && v == answer {
			return []string{PortOutcome1, PortOutcome2, PortOutcome3}[i]
		}
	}
	output["error"] = fmt.Sprintf("the model answered %q, which is not one of this node's answers", answer)
	return PortFailed
}

// agentTurns counts the steps of a run that spend tokens, this one included.
func (e *Engine) agentTurns(run dbpkg.FlowRun, g Graph) int {
	total := 0
	for _, n := range g.Nodes {
		if n.Type == TypeRunAgent || n.Type == TypeAIEvaluate || IsPluginNode(n.Type) {
			total += dbpkg.CountFlowSteps(e.db, run.ID, n.ID)
		}
	}
	return total
}
