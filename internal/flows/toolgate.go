package flows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/permissions"

	"github.com/google/uuid"
)

// A flow can answer the approval of a tool (the PreToolUse hook). The hook is waiting while the
// flow runs, so this is the one place where someone blocks on a run. Three rules follow from it:
// the wait is short, a run only uses nodes that answer quickly, and whatever goes wrong ends in
// a person being asked. A flow's failure never allows a tool.

// The answers of a Tool decision node.
const (
	ToolAllow = "allow"
	ToolDeny  = "deny"
	ToolAsk   = "ask"
)

const (
	// defaultToolTimeout comes out of the four minutes the hook waits, which leaves a person
	// at least three. It only runs out on a slow API or a stuck flow.
	defaultToolTimeout = time.Minute
	// maxToolInput: a bigger input (a long Write) goes straight to a person. Cutting it would
	// have a model judge a part of what would run, and it would be kept in full in the run.
	maxToolInput = 64 << 10
)

// nodes a Tool requested flow can use: quick, and none of them starts a turn, which would ask
// for tools of its own while the hook waits.
var toolFlowNodes = map[string]bool{
	TypeToolRequest: true, TypeToolDecision: true, TypeMatchTool: true, TypeIf: true, TypeSwitch: true, TypeStop: true,
	TypeAIEvaluate: true, TypeComment: true, TypeAddTag: true, TypeNotifySupervisor: true, TypeGetJSONProp: true,
}

// toolRuleOf reads a Match tool node as a permission rule.
func toolRuleOf(n Node) (permissions.Rule, error) {
	rule := permissions.Rule{Tool: n.str("tool")}
	if rule.Tool == "" {
		rule.Tool = "*"
	}
	for _, line := range strings.Split(n.str("input_regex"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		field, pattern, ok := strings.Cut(line, ":")
		field, pattern = strings.TrimSpace(field), strings.TrimSpace(pattern)
		if !ok || field == "" || pattern == "" {
			return rule, fmt.Errorf("the line %q is not parameter: regex", line)
		}
		if rule.InputRegex == nil {
			rule.InputRegex = map[string]string{}
		}
		if _, dup := rule.InputRegex[field]; dup {
			return rule, fmt.Errorf("the parameter %q is there twice", field)
		}
		rule.InputRegex[field] = pattern
	}
	return rule, nil
}

// matchToolStep is what a Match tool node does: the same test as a permission rule.
func matchToolStep(run dbpkg.FlowRun, n Node) (string, error) {
	if toolRequestOf(run) == "" {
		return "", errors.New("Match tool only reads the Tool requested trigger")
	}
	rule, err := toolRuleOf(n)
	if err != nil {
		return "", err
	}
	m, err := permissions.NewMatcher(rule)
	if err != nil {
		return "", err
	}
	var ev struct {
		ToolName string `json:"tool_name"`
		Input    string `json:"tool_input_text"`
	}
	if err := json.Unmarshal(run.Event, &ev); err != nil {
		return "", err
	}
	ok, err := m.Matches(ev.ToolName, json.RawMessage(ev.Input))
	if err != nil {
		return "", err
	}
	if ok {
		return PortMatch, nil
	}
	return PortNoMatch, nil
}

// ToolRequest is a tool call waiting for an answer.
type ToolRequest struct {
	ProjectID string
	CardID    string
	SessionID string
	ToolUseID string
	ToolName  string
	WorkDir   string
	Task      string // the card's title, empty without a card
	Agent     string // the agent's name, empty in a session that has none
	AutoMode  bool   // claude runs in auto mode: without a clear answer, it decides
	Input     json.RawMessage
}

// ToolVerdict is the answer. Ask means a person decides.
type ToolVerdict struct {
	Decision string
	Reason   string
}

func toolRank(decision string) int {
	switch decision {
	case ToolDeny:
		return 2
	case ToolAsk:
		return 1
	}
	return 0
}

// toolWait is one request being decided. Several flows can match it, and the strictest wins.
type toolWait struct {
	expected int // runs started, -1 until they all are
	ended    int
	runs     []string
	decided  map[string]bool
	verdict  ToolVerdict
	closed   bool
	done     chan ToolVerdict
}

func (w *toolWait) add(v ToolVerdict) {
	if w.verdict.Decision == "" || toolRank(v.Decision) > toolRank(w.verdict.Decision) {
		w.verdict = v
	}
}

// settle hands over the answer once it is known: a deny cannot get any stricter, so it does not
// wait for the other flows.
func (w *toolWait) settle() {
	if w.closed || (w.verdict.Decision != ToolDeny && (w.expected < 0 || w.ended < w.expected)) {
		return
	}
	w.closed = true
	if w.verdict.Decision == "" {
		w.verdict = ToolVerdict{Decision: ToolAsk}
	}
	w.done <- w.verdict
}

type toolWaits struct {
	mu sync.Mutex
	m  map[string]*toolWait
}

func (t *toolWaits) with(id string, f func(*toolWait)) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	w, ok := t.m[id]
	if ok {
		f(w)
	}
	return ok
}

// activeToolFlow reports whether a flow would take a tool request, without leaving a trace: most
// projects have none and this runs on every tool call.
func (e *Engine) activeToolFlow(projectID string) bool {
	if dbpkg.FlowsPaused(e.db, projectID) {
		return false
	}
	active, err := dbpkg.ActiveFlows(e.db, projectID)
	if err != nil {
		log.Printf("[flows] list flows: %v", err)
		return false
	}
	for _, f := range active {
		g, err := e.graph(f.ID, f.PublishedVersion)
		if err != nil {
			continue
		}
		for _, n := range g.Nodes {
			if n.Type == TypeToolRequest {
				return true
			}
		}
	}
	return false
}

// DecideTool lets the project's flows answer a tool request. It blocks for at most ToolTimeout
// and answers Ask when no flow applies or none gave a clear answer in time.
func (e *Engine) DecideTool(ctx context.Context, req ToolRequest) ToolVerdict {
	if !e.activeToolFlow(req.ProjectID) {
		return ToolVerdict{Decision: ToolAsk}
	}
	if len(req.Input) > maxToolInput {
		return ToolVerdict{Decision: ToolAsk, Reason: "the tool's input is too long for a flow to read"}
	}
	var input any
	if len(req.Input) > 0 {
		if err := json.Unmarshal(req.Input, &input); err != nil {
			return ToolVerdict{Decision: ToolAsk, Reason: "the tool's input could not be read"}
		}
	}

	id := uuid.New().String()
	w := &toolWait{expected: -1, decided: map[string]bool{}, done: make(chan ToolVerdict, 1)}
	e.tools.mu.Lock()
	e.tools.m[id] = w
	e.tools.mu.Unlock()

	err := dbpkg.RecordToolRequest(e.db, req.ProjectID, req.CardID, req.SessionID, map[string]any{
		"request_id": id, "tool_name": req.ToolName, "tool_use_id": req.ToolUseID, "tool_input": input,
		"tool_input_text": string(req.Input), "work_dir": req.WorkDir, "task": req.Task, "agent": req.Agent, "auto_mode": req.AutoMode,
	})
	verdict := ToolVerdict{Decision: ToolAsk, Reason: "the flow could not start"}
	stopReason := "another flow already denied this tool"
	if err == nil {
		e.Notify()
		timer := time.NewTimer(e.ToolTimeout)
		defer timer.Stop()
		select {
		case verdict = <-w.done:
		case <-timer.C:
			verdict.Reason = "the flow did not decide in time"
			stopReason = verdict.Reason + ", so a person was asked"
		case <-ctx.Done():
			verdict.Reason = "the hub is stopping"
			stopReason = verdict.Reason
		}
	}

	// From here nobody listens: runs still going stop, and no new ones start.
	e.tools.mu.Lock()
	w.closed = true
	delete(e.tools.m, id)
	runs := append([]string(nil), w.runs...)
	e.tools.mu.Unlock()
	for _, run := range runs {
		e.closeRun(run, dbpkg.RunCancelled, stopReason)
	}
	return verdict
}

// startToolRuns starts the flows for a tool request. A request nobody waits for any more (it
// timed out, or the hub restarted) starts nothing.
func (e *Engine) startToolRuns(ev dbpkg.FlowEvent) {
	id := fmt.Sprint(ev.Payload["request_id"])
	open := func() bool {
		alive := false
		e.tools.with(id, func(w *toolWait) { alive = !w.closed })
		return alive
	}
	if !open() {
		return
	}
	started := 0
	defer func() {
		e.tools.with(id, func(w *toolWait) {
			w.expected = started
			w.settle()
		})
	}()
	if dbpkg.FlowsPaused(e.db, ev.ProjectID) {
		return
	}
	flows, err := dbpkg.ActiveFlows(e.db, ev.ProjectID)
	if err != nil {
		log.Printf("[flows] list flows: %v", err)
		return
	}
	event, _ := json.Marshal(eventScope(ev))
	for _, f := range flows {
		g, err := e.graph(f.ID, f.PublishedVersion)
		if err != nil {
			log.Printf("[flows] flow %s: %v", f.ID, err)
			continue
		}
		var starts []string
		for _, n := range g.Nodes {
			if n.Type == TypeToolRequest {
				starts = append(starts, n.ID)
			}
		}
		if len(starts) == 0 || !open() {
			continue
		}
		// Never queued: the agent can ask for several tools at once, and each one has someone
		// waiting. None of these nodes moves the card, so they do not get in each other's way.
		created, err := dbpkg.CreateFlowRun(e.db, dbpkg.FlowRun{
			FlowID: f.ID, Version: f.PublishedVersion, ProjectID: ev.ProjectID, CardID: ev.CardID,
			EventID: ev.ID, Event: event, Status: dbpkg.RunRunning,
		}, starts)
		if err != nil {
			if !errors.Is(err, dbpkg.ErrFlowRunExists) {
				log.Printf("[flows] start flow %s: %v", f.ID, err)
			}
			continue
		}
		started++
		tracked := e.tools.with(id, func(w *toolWait) {
			if !w.closed {
				w.runs = append(w.runs, created.ID)
			} else {
				started--
			}
		})
		e.host.RunChanged(created)
		if !tracked || !open() { // the wait ended while this run was being created
			e.closeRun(created.ID, dbpkg.RunCancelled, "nobody is waiting for this tool request any more")
		}
	}
}

// toolRequestOf returns the request a run answers, or "" for any other kind of run.
func toolRequestOf(run dbpkg.FlowRun) string {
	var ev struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(run.Event, &ev) != nil || ev.Type != dbpkg.EventToolRequested {
		return ""
	}
	return ev.RequestID
}

// decideToolStep is what a Tool decision node does.
func (e *Engine) decideToolStep(run dbpkg.FlowRun, decision, reason string) error {
	id := toolRequestOf(run)
	if id == "" {
		return errors.New("Tool decision only answers the Tool requested trigger")
	}
	if toolRank(decision) == 0 && decision != ToolAllow {
		return fmt.Errorf("Tool decision: %q is not a decision", decision)
	}
	if reason == "" {
		reason = "decided by a flow"
	}
	if f, err := dbpkg.GetFlow(e.db, run.FlowID); err == nil {
		reason = fmt.Sprintf("Flow %q: %s", f.Name, reason)
	}
	e.tools.with(id, func(w *toolWait) {
		w.decided[run.ID] = true
		w.add(ToolVerdict{Decision: decision, Reason: reason})
	})
	return nil
}

// toolRunEnded counts a finished run. One that ended without a decision asks for a person,
// whether it failed, was cancelled or simply ran out of nodes.
func (e *Engine) toolRunEnded(run dbpkg.FlowRun) {
	id := toolRequestOf(run)
	if id == "" {
		return
	}
	e.tools.with(id, func(w *toolWait) {
		if !w.decided[run.ID] {
			reason := run.Error
			if reason == "" {
				reason = "the flow ended without a Tool decision"
			}
			w.add(ToolVerdict{Decision: ToolAsk, Reason: reason})
		}
		w.ended++
		w.settle()
	})
}

// toolFlowProblems checks the flows that answer tool requests.
func toolFlowProblems(g Graph) []Problem {
	var out []Problem
	hasTrigger, hasDecision := false, false
	for _, n := range g.Nodes {
		switch n.Type {
		case TypeToolRequest:
			hasTrigger = true
		case TypeToolDecision:
			hasDecision = true
		case TypeMatchTool:
			rule, err := toolRuleOf(n)
			if err == nil {
				_, err = permissions.NewMatcher(rule)
			}
			if err != nil {
				out = append(out, Problem{NodeID: n.ID, Message: "Match tool: " + err.Error()})
			} else if rule.Tool == "*" && len(rule.InputRegex) == 0 {
				out = append(out, Problem{NodeID: n.ID, Message: "Match tool matches everything: write a tool or a parameter line"})
			}
		}
	}
	if !hasTrigger {
		for _, n := range g.Nodes {
			switch n.Type {
			case TypeToolDecision:
				out = append(out, Problem{NodeID: n.ID, Message: "Tool decision only works in a flow started by Tool requested: nothing else is waiting for its answer"})
			case TypeMatchTool:
				out = append(out, Problem{NodeID: n.ID, Message: "Match tool only works in a flow started by Tool requested: there is no tool to read"})
			}
		}
		return out
	}
	for _, n := range g.Nodes {
		def, ok := NodeTypeOf(n.Type)
		if !ok || toolFlowNodes[n.Type] {
			continue
		}
		if def.Category == CategoryTrigger {
			out = append(out, Problem{NodeID: n.ID, Message: fmt.Sprintf("%s cannot share a flow with Tool requested: put each in its own flow", def.Title)})
		} else {
			out = append(out, Problem{NodeID: n.ID, Message: fmt.Sprintf("%s cannot be used with Tool requested: the agent is waiting for the answer, so only quick nodes that start no turn are allowed", def.Title)})
		}
	}
	if !hasDecision {
		out = append(out, Problem{Message: "this flow has no Tool decision, so it could only ever end up asking a person"})
	}

	// Allowing with nothing checked is never what someone meant: agents that should not ask
	// have auto mode, which keeps Claude's own checks.
	stops := map[string]bool{TypeIf: true, TypeSwitch: true, TypeAIEvaluate: true, TypeMatchTool: true}
	seen := map[string]bool{}
	var walk func(string)
	walk = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		n, ok := g.node(id)
		if !ok {
			return
		}
		if n.Type == TypeToolDecision && n.str("decision") == ToolAllow {
			out = append(out, Problem{NodeID: id, Message: "this allows every tool without checking anything: put a Match tool or a condition before it"})
		}
		if stops[n.Type] {
			return
		}
		for _, e := range g.Edges {
			if e.From == id {
				walk(e.To)
			}
		}
	}
	for _, n := range g.Nodes {
		if n.Type == TypeToolRequest {
			walk(n.ID)
		}
	}
	return out
}
