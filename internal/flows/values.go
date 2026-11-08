package flows

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"

	dbpkg "claude-orchestrator/internal/db"
)

// Values travel on the edges: the answer a node gives through an output is what the nodes
// connected to it receive on their input. A node only reads its own input ({{input}} in
// its fields, as text), plus the run's card, event and run. A JSON answer is not opened as
// variables: a Get JSON prop node reads one property of it.

// stepInput is the value that reached a step; nil for a trigger.
func stepInput(s dbpkg.FlowStep) any {
	if len(s.Input) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(s.Input, &v); err != nil {
		return nil
	}
	return v
}

// valueText is a value as a field reads it: text stays text, anything else is its JSON.
func valueText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(raw)
}

// jsonPropRe finds the {json.a.b} references of a Get JSON prop node.
var jsonPropRe = regexp.MustCompile(`\{json((?:\.[^.{}\s]+)*)\}`)

// CheckJSONProp reports whether the text of a Get JSON prop node reads something.
func CheckJSONProp(text string) error {
	if !jsonPropRe.MatchString(text) {
		return errors.New("write at least one {json.prop}, such as {json.summary}")
	}
	return nil
}

// asJSON reads the input of a Get JSON prop node. Text has to be JSON.
func asJSON(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any, []any:
		return x, nil
	case string:
		var out any
		if err := json.Unmarshal([]byte(strings.TrimSpace(x)), &out); err != nil {
			return nil, errors.New("Get JSON prop: its input is not JSON")
		}
		switch out.(type) {
		case map[string]any, []any:
			return out, nil
		}
	}
	return nil, errors.New("Get JSON prop: its input is not a JSON object or list")
}

// jsonPath follows .a.b.0 into a JSON value.
func jsonPath(root any, path string) (any, error) {
	cur := root
	walked := "json"
	for _, key := range strings.Split(strings.TrimPrefix(path, "."), ".") {
		if key == "" {
			continue
		}
		walked += "." + key
		switch x := cur.(type) {
		case map[string]any:
			v, ok := x[key]
			if !ok {
				return nil, fmt.Errorf("Get JSON prop: %s is not there", walked)
			}
			cur = v
		case []any:
			i, err := strconv.Atoi(key)
			if err != nil || i < 0 || i >= len(x) {
				return nil, fmt.Errorf("Get JSON prop: %s is not there: the list has %d items", walked, len(x))
			}
			cur = x[i]
		default:
			return nil, fmt.Errorf("Get JSON prop: %s is not there: %s is not an object", walked, strings.TrimSuffix(walked, "."+key))
		}
	}
	return cur, nil
}

// GetJSONProp is what a Get JSON prop node gives. A text that is only one {json...} keeps the
// value as it is; mixed with other text, every reference is written in as text.
func GetJSONProp(input any, text string) (any, error) {
	root, err := asJSON(input)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(text)
	if m := jsonPropRe.FindStringSubmatch(trimmed); m != nil && m[0] == trimmed {
		return jsonPath(root, m[1])
	}
	var firstErr error
	out := jsonPropRe.ReplaceAllStringFunc(text, func(ref string) string {
		v, err := jsonPath(root, jsonPropRe.FindStringSubmatch(ref)[1])
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return valueText(v)
	})
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// ---- graphs saved before values travelled on the edges ----

// oldNextPort is the output every step used to continue through.
const oldNextPort = "next"

var oldNodeRefRe = regexp.MustCompile(`nodes\.([A-Za-z0-9_]+)\.([A-Za-z0-9_]+)`)

// MigrateGraph brings a graph saved with the old "next" outputs and nodes.<id> references up to
// date. An edge from "next" goes from the node's first output instead. A reference to what the
// node right before gave on that edge, such as nodes.n2.error on a "failed" edge, becomes input.
// Anything else stays as it was, and publishing says what to change.
func MigrateGraph(g Graph) (Graph, bool) {
	changed := false
	for i, e := range g.Edges {
		from, ok := g.node(e.From)
		if !ok || e.Port != oldNextPort {
			continue
		}
		def, ok := NodeTypeOf(from.Type)
		if !ok || contains(def.Ports, oldNextPort) || len(def.Ports) == 0 {
			continue
		}
		g.Edges[i].Port = def.Ports[0]
		changed = true
	}
	for i, n := range g.Nodes {
		for k, v := range n.Config {
			text, ok := v.(string)
			if !ok || !strings.Contains(text, "nodes.") {
				continue
			}
			next := oldNodeRefRe.ReplaceAllStringFunc(text, func(ref string) string {
				m := oldNodeRefRe.FindStringSubmatch(ref)
				if oldRefIsInput(g, m[1], m[2], n.ID) {
					return "input"
				}
				return ref
			})
			if next != text {
				g.Nodes[i].Config[k] = next
				changed = true
			}
		}
	}
	return g, changed
}

// oldRefIsInput: every edge from src into node carries the value nodes.<src>.<field> named.
func oldRefIsInput(g Graph, src, field, node string) bool {
	from, ok := g.node(src)
	if !ok {
		return false
	}
	found := false
	for _, e := range g.Edges {
		if e.From != src || e.To != node {
			continue
		}
		found = true
		switch {
		case e.Port == PortFailed && field == "error":
		case from.Type == TypeAIEvaluate && field == "reason" && e.Port != PortFailed:
		case from.Type == TypeComment && field == "text", from.Type == TypeNotifySupervisor && field == "text":
		case from.Type == TypeAddTag && field == "tag", from.Type == TypeCreateTask && field == "card_id":
		case from.Type == TypeMoveCard && e.Port == PortRejected && field == "reason":
		// a plugin's output named after what it gives, such as Gemini's "answer"
		case IsPluginNode(from.Type) && e.Port == field:
		default:
			return false
		}
	}
	return found
}

// MigrateStoredGraphs updates the drafts and the published versions of every flow once. Old
// runs keep the graph they ran with: a version is rewritten in place, which only renames ports.
func MigrateStoredGraphs(db *sql.DB) {
	fix := func(raw string) (string, bool) {
		var g Graph
		if err := json.Unmarshal([]byte(raw), &g); err != nil {
			return raw, false
		}
		g, changed := MigrateGraph(g)
		if !changed {
			return raw, false
		}
		out, err := json.Marshal(g)
		if err != nil {
			return raw, false
		}
		return string(out), true
	}
	type row struct {
		key1 string
		key2 int
		raw  string
	}
	read := func(q string) []row {
		rows, err := db.Query(q)
		if err != nil {
			log.Printf("[flows] migrate graphs: %v", err)
			return nil
		}
		defer rows.Close()
		var out []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.key1, &r.key2, &r.raw); err == nil {
				out = append(out, r)
			}
		}
		return out
	}
	for _, r := range read(`SELECT id, 0, draft FROM flows WHERE draft LIKE '%"next"%' OR draft LIKE '%nodes.%'`) {
		if raw, ok := fix(r.raw); ok {
			if _, err := db.Exec(`UPDATE flows SET draft=? WHERE id=?`, raw, r.key1); err != nil {
				log.Printf("[flows] migrate draft %s: %v", r.key1, err)
			}
		}
	}
	for _, r := range read(`SELECT flow_id, version, graph FROM flow_versions WHERE graph LIKE '%"next"%' OR graph LIKE '%nodes.%'`) {
		if raw, ok := fix(r.raw); ok {
			if _, err := db.Exec(`UPDATE flow_versions SET graph=? WHERE flow_id=? AND version=?`, raw, r.key1, r.key2); err != nil {
				log.Printf("[flows] migrate version %s@%d: %v", r.key1, r.key2, err)
			}
		}
	}
}
