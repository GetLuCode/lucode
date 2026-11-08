package flows

import (
	"encoding/json"
	"strings"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
)

func TestGetJSONProp(t *testing.T) {
	in := map[string]any{"branch": "task/x", "ahead": float64(2), "files": []any{"a.go", "b.go"}, "git": map[string]any{"base": "main"}}
	cases := []struct {
		input any
		text  string
		want  any
		err   string
	}{
		{in, "{json.branch}", "task/x", ""},
		{in, "{json.ahead}", float64(2), ""}, // alone it keeps its type
		{in, "{json.git.base}", "main", ""},
		{in, "{json.files.1}", "b.go", ""},
		{in, "{json.git}", map[string]any{"base": "main"}, ""},
		{in, "Branch {json.branch} is {json.ahead} ahead of {json.git.base}", "Branch task/x is 2 ahead of main", ""},
		{in, "files: {json.files}", `files: ["a.go","b.go"]`, ""},
		{`{"summary":"done"}`, "{json.summary}", "done", ""}, // JSON text is read too
		{in, "{json.nope}", nil, "json.nope is not there"},
		{in, "{json.files.7}", nil, "the list has 2 items"},
		{in, "{json.branch.x}", nil, "is not an object"},
		{"just text", "{json.a}", nil, "not JSON"},
		{float64(3), "{json.a}", nil, "not a JSON object"},
	}
	for _, c := range cases {
		got, err := GetJSONProp(c.input, c.text)
		if c.err != "" {
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Errorf("%v %q: want error %q, got %v %v", c.input, c.text, c.err, got, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.text, err)
			continue
		}
		a, _ := json.Marshal(got)
		b, _ := json.Marshal(c.want)
		if string(a) != string(b) {
			t.Errorf("%q: got %s, want %s", c.text, a, b)
		}
	}
}

func TestValuesTravelOnEdges(t *testing.T) {
	h := newHarness(t)
	f := h.publish(Graph{
		Nodes: []Node{
			{ID: "t", Type: TypeTaskDone},
			{ID: "j", Type: TypeGetJSONProp, Config: cfg("template", "{json.summary}")},
			{ID: "if", Type: TypeIf, Config: cfg("condition", `input.startsWith("ok")`)},
			{ID: "yes", Type: TypeComment, Config: cfg("text", "said {{input}}")},
			{ID: "no", Type: TypeComment, Config: cfg("text", "no")},
			{ID: "tag", Type: TypeAddTag, Config: cfg("tag", "{{input}}-seen")},
		},
		Edges: []Edge{{"t", PortEvent, "j"}, {"j", PortValue, "if"}, {"if", PortTrue, "yes"}, {"if", PortFalse, "no"}, {"yes", PortText, "tag"}},
	})
	h.event(dbpkg.EventTaskDone, "c1", "", map[string]any{"summary": "ok, all green"})
	if got := h.onlyRun(f); got.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
	if len(h.host.comments) != 1 || h.host.comments[0] != "said ok, all green" {
		t.Fatalf("comments: %q", h.host.comments)
	}
	if len(h.host.tags) != 1 || h.host.tags[0] != "said ok, all green-seen" {
		t.Fatalf("tags: %q", h.host.tags)
	}
}

func TestGetJSONPropFailsOnText(t *testing.T) {
	// text into a JSON input is refused when publishing
	g := Graph{
		Nodes: []Node{reviewTrigger(), {ID: "c", Type: TypeComment, Config: cfg("text", "plain")}, {ID: "j", Type: TypeGetJSONProp, Config: cfg("template", "{json.x}")}},
		Edges: []Edge{{"t", PortEvent, "c"}, {"c", PortText, "j"}},
	}
	var all []string
	for _, p := range Validate(g) {
		all = append(all, p.Message)
	}
	if !strings.Contains(strings.Join(all, " | "), "needs JSON") {
		t.Fatalf("text into a JSON input was accepted: %q", all)
	}

	// through an output that carries anything it is checked when it runs, and takes "failed"
	h2 := newHarness(t)
	f2 := h2.publish(Graph{
		Nodes: []Node{
			reviewTrigger(),
			{ID: "c", Type: TypeComment, Config: cfg("text", "plain")},
			{ID: "d", Type: TypeDelay, Config: cfg("seconds", float64(0))},
			{ID: "j", Type: TypeGetJSONProp, Config: cfg("template", "{json.x}")},
			{ID: "bad", Type: TypeComment, Config: cfg("text", "could not: {{input}}")},
		},
		Edges: []Edge{{"t", PortEvent, "c"}, {"c", PortText, "d"}, {"d", PortOutput, "j"}, {"j", PortFailed, "bad"}},
	})
	h2.event(dbpkg.EventCardMoved, "c1", "", moved("review"))
	h2.e.Drain()
	if got := h2.onlyRun(f2); got.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
	if want := "could not: Get JSON prop: its input is not JSON"; len(h2.host.comments) != 2 || h2.host.comments[1] != want {
		t.Fatalf("comments %q, want %q last", h2.host.comments, want)
	}
}

func TestValidateGetJSONPropAndOldReferences(t *testing.T) {
	trigger := Node{ID: "t", Type: TypeManual}
	msgs := func(g Graph) string {
		var all []string
		for _, p := range Validate(g) {
			all = append(all, p.Message)
		}
		return strings.Join(all, " | ")
	}
	noRef := Graph{Nodes: []Node{trigger, {ID: "j", Type: TypeGetJSONProp, Config: cfg("template", "summary")}}, Edges: []Edge{{"t", PortEvent, "j"}}}
	if !strings.Contains(msgs(noRef), "at least one {json.prop}") {
		t.Fatalf("a Get JSON prop that reads nothing was accepted: %s", msgs(noRef))
	}
	old := Graph{Nodes: []Node{trigger, {ID: "c", Type: TypeComment, Config: cfg("text", "{{nodes.x.branch}}")}}, Edges: []Edge{{"t", PortEvent, "c"}}}
	if !strings.Contains(msgs(old), "nodes.<id> is gone") {
		t.Fatalf("an old nodes reference: %s", msgs(old))
	}
	ok := Graph{Nodes: []Node{trigger, {ID: "j", Type: TypeGetJSONProp, Config: cfg("template", "{json.type}")}}, Edges: []Edge{{"t", PortEvent, "j"}}}
	if got := msgs(ok); got != "" {
		t.Fatalf("a trigger's JSON into Get JSON prop: %s", got)
	}
}

// A graph saved before values travelled on the edges: "next" becomes the node's own output and
// what the node right before gave on that edge becomes input.
func TestMigrateOldGraph(t *testing.T) {
	raw := `{"nodes":[
		{"id":"n1","type":"trigger.task_done","config":{}},
		{"id":"n2","type":"action.git_status","config":{}},
		{"id":"n3","type":"action.move_card","config":{"to_kind":"review"}},
		{"id":"n4","type":"action.notify_supervisor","config":{"text":"done: {{event.summary}}"}},
		{"id":"n7","type":"action.comment","config":{"text":"Could not read it: {{nodes.n2.error}}"}},
		{"id":"n8","type":"action.move_card","config":{"to_kind":"review"}},
		{"id":"n9","type":"action.comment","config":{"text":"on {{nodes.n2.branch}}"}}
	],"edges":[
		{"from":"n1","port":"next","to":"n2"},
		{"from":"n2","port":"clean","to":"n3"},{"from":"n3","port":"next","to":"n4"},
		{"from":"n2","port":"failed","to":"n7"},{"from":"n7","port":"next","to":"n8"},
		{"from":"n2","port":"dirty","to":"n9"}
	]}`
	g, err := ParseGraph([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	ports := map[string]string{}
	for _, e := range g.Edges {
		ports[e.From+">"+e.To] = e.Port
	}
	for k, want := range map[string]string{"n1>n2": PortEvent, "n3>n4": PortMoved, "n7>n8": PortText, "n2>n3": PortClean} {
		if ports[k] != want {
			t.Errorf("%s: %q, want %q", k, ports[k], want)
		}
	}
	text := func(id string) string { n, _ := g.node(id); return n.str("text") }
	if got := text("n7"); got != "Could not read it: {{input}}" {
		t.Errorf("n7: %q", got)
	}
	if got := text("n9"); got != "on {{nodes.n2.branch}}" { // not what the edge carries: publishing says so
		t.Errorf("n9: %q", got)
	}

	// and in the database, once at start
	h := newHarness(t)
	fl, err := dbpkg.InsertFlow(h.e.db, dbpkg.Flow{ProjectID: "p1", Name: "old", Draft: json.RawMessage(raw)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbpkg.PublishFlow(h.e.db, fl.ID, json.RawMessage(raw)); err != nil {
		t.Fatal(err)
	}
	MigrateStoredGraphs(h.e.db)
	fl, _ = dbpkg.GetFlow(h.e.db, fl.ID)
	v, _ := dbpkg.GetFlowVersion(h.e.db, fl.ID, 1)
	for name, stored := range map[string]string{"draft": string(fl.Draft), "version": string(v)} {
		if strings.Contains(stored, `"next"`) || !strings.Contains(stored, `"moved"`) {
			t.Errorf("%s not migrated: %s", name, stored)
		}
	}
}

func TestBlueprintsValidate(t *testing.T) {
	for _, b := range Blueprints() {
		if p := Validate(b.Graph); len(p) != 0 {
			t.Errorf("%s: %+v", b.Key, p)
		}
	}
}

func TestTaskFinishedBlueprintRuns(t *testing.T) {
	h := newHarness(t)
	b, _ := BlueprintOf(BlueprintTaskFinished)
	f := h.publish(b.Graph)
	h.host.gitErr = errNoPluginHost // any failure
	h.event(dbpkg.EventTaskDone, "c1", "", map[string]any{"summary": "it works"})
	h.settleCalls()
	if got := h.onlyRun(f); got.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
	if len(h.host.comments) != 1 || !strings.HasSuffix(h.host.comments[0], "waits in Review: "+errNoPluginHost.Error()) {
		t.Fatalf("comments: %q", h.host.comments)
	}

	h2 := newHarness(t)
	f2 := h2.publish(b.Graph)
	h2.host.git = GitState{State: PortNothing}
	h2.event(dbpkg.EventTaskDone, "c1", "", map[string]any{"summary": "it works"})
	h2.settleCalls()
	if got := h2.onlyRun(f2); got.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", got.Status, got.Error)
	}
	if len(h2.host.notified) != 1 || h2.host.notified[0] != "Task Fix it is done: it works" || strings.Join(h2.host.moves, ",") != "done" {
		t.Fatalf("notified %q, moves %q", h2.host.notified, h2.host.moves)
	}
}
