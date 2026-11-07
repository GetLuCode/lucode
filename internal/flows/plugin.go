package flows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	dbpkg "claude-orchestrator/internal/db"
)

// Nodes that plugins add. The registry asks a source for them, so the validator, the engine and
// the editor treat them like the base nodes. Their code runs away from the engine's goroutine,
// parked on a wait like any other slow step.

const (
	// PluginNodePrefix starts the type of a plugin's node: plugin.<plugin id>.<node id>.
	PluginNodePrefix = "plugin."
	pluginWaitPrefix = "plugin:"
	// pluginStepGrace is how much later than the node's own deadline the step's timer goes off.
	pluginStepGrace = 30 * time.Second
	// maxPluginStep is the timer of a node nobody knows the timeout of any more.
	maxPluginStep = 15 * time.Minute
)

// PluginCall runs a plugin's node.
type PluginCall struct {
	Type      string
	Config    map[string]any
	Card      map[string]any
	Event     map[string]any
	Nodes     map[string]any
	Run       map[string]any
	ProjectID string
	CardID    string
	FlowID    string
	RunID     string
	Depth     int
}

// PluginSource is what the engine needs from the plugin host.
type PluginSource interface {
	NodeTypes() []NodeType
	Blueprints() []Blueprint
	Installed(pluginID string) bool
	RunNode(ctx context.Context, call PluginCall) (port string, output map[string]any, err error)
}

type pluginHolder struct{ s PluginSource }

var pluginSource atomic.Pointer[pluginHolder]

// SetPluginSource connects the plugin host. nil disconnects it.
func SetPluginSource(s PluginSource) {
	if s == nil {
		pluginSource.Store(nil)
		return
	}
	pluginSource.Store(&pluginHolder{s})
}

func currentPlugins() PluginSource {
	if h := pluginSource.Load(); h != nil {
		return h.s
	}
	return nil
}

// IsPluginNode reports whether a node type comes from a plugin.
func IsPluginNode(t string) bool { return strings.HasPrefix(t, PluginNodePrefix) }

// pluginOfNode reads the plugin's id out of a node type: a node id has no dots.
func pluginOfNode(t string) string {
	rest := strings.TrimPrefix(t, PluginNodePrefix)
	if i := strings.LastIndex(rest, "."); i > 0 {
		return rest[:i]
	}
	return rest
}

func pluginWaitKey(seq int64) string { return fmt.Sprintf("%s%d", pluginWaitPrefix, seq) }

// reserved keys of a call's result: an output with these could wake another step.
var reservedOutputKeys = []string{"wait_key", "port"}

// startPluginNode parks the step and runs the node in the background. The answer comes back as an
// event, in order, like every other slow node. No lock is held meanwhile.
func (e *Engine) startPluginNode(run dbpkg.FlowRun, g Graph, node Node, s dbpkg.FlowStep, scope Scope) (result, error) {
	if turns := e.agentTurns(run, g); turns > MaxAgentTurns {
		return result{}, fmt.Errorf("the run reached the limit of %d agent or AI turns", MaxAgentTurns)
	}
	src := currentPlugins()
	def, known := NodeTypeOf(node.Type)
	if src == nil || !known {
		return result{}, fmt.Errorf("the plugin %s is not installed", pluginOfNode(node.Type))
	}
	config := map[string]any{}
	for _, f := range def.Fields {
		v, has := node.Config[f.Name]
		if !has {
			if f.Default != nil {
				v = f.Default
			} else {
				continue
			}
		}
		text := node.str(f.Name)
		if !has {
			text = fmt.Sprint(v)
		}
		switch f.Kind {
		case FieldTemplate:
			out, err := RenderTemplate(text, scope)
			if err != nil {
				return result{}, fmt.Errorf("%s: %s: %w", def.Title, f.Label, err)
			}
			v = out
		case FieldExpr:
			out, err := EvalString(text, scope)
			if err != nil {
				return result{}, fmt.Errorf("%s: %s: %w", def.Title, f.Label, err)
			}
			v = out
		}
		config[f.Name] = v
	}
	timeout := maxPluginStep
	if def.TimeoutSeconds > 0 {
		timeout = time.Duration(def.TimeoutSeconds) * time.Second
	}
	wake := e.now().Add(timeout + pluginStepGrace).UTC().Format("2006-01-02T15:04:05.000Z")
	key := pluginWaitKey(s.Seq)
	if err := dbpkg.WaitFlowStep(e.db, s.Seq, key, wake, json.RawMessage(`{}`)); err != nil {
		return result{}, err
	}
	asMap := func(v any) map[string]any {
		m, _ := v.(map[string]any)
		return m
	}
	call := PluginCall{
		Type: node.Type, Config: config, Card: asMap(scope["card"]), Event: asMap(scope["event"]),
		Nodes: asMap(scope["nodes"]), Run: asMap(scope["run"]), ProjectID: run.ProjectID, CardID: run.CardID,
		FlowID: run.FlowID, RunID: run.ID, Depth: run.Depth,
	}
	e.startCall(run, key, func(ctx context.Context) (string, map[string]any, error) {
		port, output, err := src.RunNode(ctx, call)
		if err != nil {
			return "", nil, err
		}
		for _, k := range reservedOutputKeys {
			delete(output, k)
		}
		return port, output, nil
	})
	e.host.RunChanged(run)
	return result{parked: true}, nil
}

// recoverPluginSteps answers the plugin nodes a stop interrupted. Their code may have acted, so
// nothing runs again: the node takes its "failed" output and says why.
func (e *Engine) recoverPluginSteps() error {
	steps, err := dbpkg.WaitingFlowStepsWithPrefix(e.db, pluginWaitPrefix)
	if err != nil {
		return err
	}
	for _, s := range steps {
		e.resumeStep(s, PortFailed, map[string]any{"error": "the hub stopped while this plugin node was running: it may or may not have finished, and nothing was retried"})
	}
	return nil
}

// pluginProblems names the plugin when a graph uses one that is gone.
func pluginProblem(n Node) (Problem, bool) {
	if !IsPluginNode(n.Type) {
		return Problem{}, false
	}
	return Problem{NodeID: n.ID, Message: fmt.Sprintf("this node comes from the plugin %s, which is not installed", pluginOfNode(n.Type))}, true
}

func pluginEventProblem(n Node) (Problem, bool) {
	if n.Type != TypePluginEvent {
		return Problem{}, false
	}
	id := n.str("plugin")
	if id == "" {
		return Problem{}, false // the required field already says so
	}
	if src := currentPlugins(); src != nil && src.Installed(id) {
		return Problem{}, false
	}
	return Problem{NodeID: n.ID, Message: fmt.Sprintf("this trigger waits for the plugin %s, which is not installed", id)}, true
}

// errNoPluginHost is what a turn or a call sees on an instance without the host.
var errNoPluginHost = errors.New("plugins are not running on this instance")

// ErrNoPluginHost is that error for callers outside this package.
func ErrNoPluginHost() error { return errNoPluginHost }
