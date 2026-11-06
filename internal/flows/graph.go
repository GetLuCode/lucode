// Package flows runs the automations of a project: graphs of nodes that react to what
// happens on the board. The hub only orchestrates here. Conditions are pure expressions,
// and anything that runs claude goes to a runner through the Host.
package flows

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Graph is the stored form of a flow. It belongs to lucode and not to the editor, so
// the editor can change without touching saved flows.
type Graph struct {
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// Node is a step of a flow. Config holds the values of its fields.
type Node struct {
	ID     string         `json:"id"`
	Type   string         `json:"type"`
	X      float64        `json:"x"`
	Y      float64        `json:"y"`
	Config map[string]any `json:"config"`
}

// Edge connects an output port of a node to the next node. Data does not travel on
// edges: every expression can read the output of any earlier node.
type Edge struct {
	From string `json:"from"`
	Port string `json:"port"`
	To   string `json:"to"`
}

// ParseGraph reads a stored graph.
func ParseGraph(raw []byte) (Graph, error) {
	var g Graph
	if err := json.Unmarshal(raw, &g); err != nil {
		return g, fmt.Errorf("invalid graph: %w", err)
	}
	return g, nil
}

func (g Graph) node(id string) (Node, bool) {
	for _, n := range g.Nodes {
		if n.ID == id {
			return n, true
		}
	}
	return Node{}, false
}

// next are the nodes connected to a port of a node.
func (g Graph) next(id, port string) []string {
	var out []string
	for _, e := range g.Edges {
		if e.From == id && e.Port == port {
			out = append(out, e.To)
		}
	}
	return out
}

// incoming are the distinct nodes with an edge into a node.
func (g Graph) incoming(id string) []string {
	seen := map[string]bool{}
	var out []string
	for _, e := range g.Edges {
		if e.To == id && !seen[e.From] {
			seen[e.From] = true
			out = append(out, e.From)
		}
	}
	return out
}

func (n Node) str(field string) string {
	switch v := n.Config[field].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	}
	return ""
}

func (n Node) boolean(field string, def bool) bool {
	if v, ok := n.Config[field].(bool); ok {
		return v
	}
	return def
}

func (n Node) num(field string, def float64) float64 {
	switch v := n.Config[field].(type) {
	case float64:
		return v
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return def
}

// Problem is something that stops a graph from being published.
type Problem struct {
	NodeID  string `json:"node_id,omitempty"`
	Message string `json:"message"`
}

// Validate checks everything that can be known before running: the types and their
// required fields, the edges, the expressions and the shape of the graph.
func Validate(g Graph) []Problem {
	problems := []Problem{}
	add := func(node, format string, args ...any) {
		problems = append(problems, Problem{NodeID: node, Message: fmt.Sprintf(format, args...)})
	}

	seen := map[string]bool{}
	triggers := 0
	for _, n := range g.Nodes {
		if n.ID == "" {
			add("", "a node has no id")
			continue
		}
		if seen[n.ID] {
			add(n.ID, "duplicate node id %q", n.ID)
		}
		seen[n.ID] = true
		def, ok := NodeTypeOf(n.Type)
		if !ok {
			add(n.ID, "unknown node type %q", n.Type)
			continue
		}
		if def.Category == CategoryTrigger {
			triggers++
		}
		for _, f := range def.Fields {
			val := n.str(f.Name)
			if f.Required && val == "" {
				add(n.ID, "%s: %s is required", def.Title, f.Label)
				continue
			}
			if val == "" {
				continue
			}
			switch f.Kind {
			case FieldExpr:
				if err := CheckExpr(val); err != nil {
					add(n.ID, "%s: %s: %v", def.Title, f.Label, err)
				}
			case FieldTemplate:
				if err := CheckTemplate(val); err != nil {
					add(n.ID, "%s: %s: %v", def.Title, f.Label, err)
				}
			case FieldSelect:
				if !contains(f.Options, val) {
					add(n.ID, "%s: %s: %q is not an option", def.Title, f.Label, val)
				}
			case FieldNumber:
				v := n.num(f.Name, -1)
				if _, err := strconv.ParseFloat(val, 64); err != nil || (f.Min != nil && v < *f.Min) {
					add(n.ID, "%s: %s must be a number%s", def.Title, f.Label, minSuffix(f.Min))
				}
			}
		}
	}
	for _, n := range g.Nodes {
		if n.Type != TypeAIEvaluate {
			continue
		}
		seenAnswer := map[string]bool{}
		for _, f := range []string{"outcome1", "outcome2", "outcome3"} {
			if v := n.str(f); v != "" {
				if seenAnswer[v] {
					add(n.ID, "AI evaluate: the answer %q is there twice", v)
				}
				seenAnswer[v] = true
			}
		}
		if n.str("outcome3") == "" && len(g.next(n.ID, PortOutcome3)) > 0 {
			add(n.ID, "AI evaluate: the third output is connected but Answer 3 is empty")
		}
	}
	if len(g.Nodes) == 0 {
		add("", "the flow is empty")
		return problems
	}
	if triggers == 0 {
		add("", "the flow needs a trigger: nothing would ever start it")
	}

	for _, e := range g.Edges {
		from, okFrom := g.node(e.From)
		to, okTo := g.node(e.To)
		if !okFrom || !okTo {
			add("", "an edge connects a node that does not exist (%s → %s)", e.From, e.To)
			continue
		}
		if def, ok := NodeTypeOf(from.Type); ok && !contains(def.Ports, e.Port) {
			add(from.ID, "%s has no output %q", def.Title, e.Port)
		}
		if def, ok := NodeTypeOf(to.Type); ok && def.Category == CategoryTrigger {
			add(to.ID, "%s is a trigger: nothing can connect into it", def.Title)
		}
	}

	problems = append(problems, mergeProblems(g)...)
	problems = append(problems, toolFlowProblems(g)...)

	if cycle := findCycle(g); cycle != "" {
		add(cycle, "this node is in a loop: loops must go through the \"again\" output of a Retry node, which limits them")
	}

	// Every node must be reachable from a trigger, or it would never run.
	reached := map[string]bool{}
	var walk func(string)
	walk = func(id string) {
		if reached[id] {
			return
		}
		reached[id] = true
		for _, e := range g.Edges {
			if e.From == id {
				walk(e.To)
			}
		}
	}
	for _, n := range g.Nodes {
		if def, ok := NodeTypeOf(n.Type); ok && def.Category == CategoryTrigger {
			walk(n.ID)
		}
	}
	for _, n := range g.Nodes {
		def, ok := NodeTypeOf(n.Type)
		if ok && !reached[n.ID] && triggers > 0 {
			add(n.ID, "%s is not connected to a trigger, so it would never run", def.Title)
		}
	}
	return problems
}

// mergeProblems checks the Merge nodes. Agents are not allowed to merge and a person does it
// from Review, so a flow merges on its own only if someone said so on that node: any other
// path to a Merge has to go through the "approved" output of a Human approval.
func mergeProblems(g Graph) []Problem {
	types := map[string]string{}
	for _, n := range g.Nodes {
		types[n.ID] = n.Type
	}
	reached := map[string]bool{}
	var walk func(string)
	walk = func(id string) {
		if reached[id] {
			return
		}
		reached[id] = true
		for _, e := range g.Edges {
			if e.From == id && !(types[id] == TypeApproval && e.Port == PortApproved) {
				walk(e.To)
			}
		}
	}
	for _, n := range g.Nodes {
		if def, ok := NodeTypeOf(n.Type); ok && def.Category == CategoryTrigger {
			walk(n.ID)
		}
	}
	var out []Problem
	for _, n := range g.Nodes {
		if n.Type != TypeMerge {
			continue
		}
		if reached[n.ID] && !n.boolean("unattended", false) {
			out = append(out, Problem{NodeID: n.ID, Message: "Merge can be reached without a person approving it: put the \"approved\" output of a Human approval before it, or turn on \"Merge without a person\""})
		}
		if n.boolean("delete_branch", true) && !n.boolean("delete_worktree", true) {
			out = append(out, Problem{NodeID: n.ID, Message: "Merge: to delete the branch the worktree has to be deleted too, because git keeps a branch a worktree is using"})
		}
	}
	return out
}

func minSuffix(min *float64) string {
	if min == nil {
		return ""
	}
	return fmt.Sprintf(" of at least %v", *min)
}

// findCycle returns a node of a loop that does not go through Retry's "again" output.
func findCycle(g Graph) string {
	types := map[string]string{}
	for _, n := range g.Nodes {
		types[n.ID] = n.Type
	}
	adj := map[string][]string{}
	for _, e := range g.Edges {
		if types[e.From] == TypeRetry && e.Port == PortAgain {
			continue
		}
		adj[e.From] = append(adj[e.From], e.To)
	}
	state := map[string]int{} // 1 = on the current path, 2 = finished
	var visit func(string) string
	visit = func(id string) string {
		state[id] = 1
		for _, to := range adj[id] {
			if state[to] == 1 {
				return to
			}
			if state[to] == 0 {
				if c := visit(to); c != "" {
					return c
				}
			}
		}
		state[id] = 2
		return ""
	}
	ids := make([]string, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		ids = append(ids, n.ID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if state[id] == 0 {
			if c := visit(id); c != "" {
				return c
			}
		}
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
