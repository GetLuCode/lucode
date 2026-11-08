package compile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dop251/goja"

	"claude-orchestrator/internal/plugins/surface"
)

const testManifest = `{
  "id": "acme.counter", "name": "Counter", "version": "1.0.0", "sdk": 1,
  "targets": { "server": { "main": "dist/server.js", "permissions": { "storage": true, "flows": true } } },
  "contributes": {
    "pages": [{ "id": "main", "label": "Counter", "ui": "dist/main.js", "rpc": ["bump"] }],
    "settings": [{ "key": "step", "label": "Step", "type": "number", "default": 1 }],
    "flowNodes": [{ "id": "read", "label": "Read", "ports": ["next"] }]
  },
  "build": { "server": "src/server.ts", "pages": { "main": "src/ui/main.tsx" } }
}`

const testServer = `
const sdk = lucode as any;
const read = (p: string): number => sdk.storage.get("n/" + p) ?? 0;
export const rpc = {
  bump(args: any) {
    const n = read(args.projectId) + Number(sdk.settings.get("step"));
    sdk.storage.set("n/" + args.projectId, n);
    sdk.flows.emit("bumped", { n });
    return { n };
  },
  hidden() { return "not for the page"; },
};
export const nodes = {
  read(input: any) { return { port: "next", output: { n: read(input.projectId) } }; },
};
`

const testPage = `
import { render } from "preact";
import { useState } from "preact/hooks";
import { rpc, toast, context } from "@lucode/ui";
import "./style.css";

function App() {
  const [n, setN] = useState(0);
  const [err, setErr] = useState("");
  return (
    <div class="counter" style={{ gap: "8px" }}>
      <output>{n}</output>
      <button onClick={async () => { const r = await rpc<{ n: number }>("bump"); setN(r.n); if (r.n >= 2) toast("two"); }}>Bump</button>
      <button class="sneaky" onClick={() => rpc("hidden").catch(e => setErr(e.message))}>Sneak</button>
      <a href="https://example.com/docs">Docs</a>
      <p class="project">{context.projectId}</p>
      {err && <p class="err">{err}</p>}
    </div>
  );
}
render(<App />, document.body);
`

const testFile = `
test("the page bumps through its backend", () => {
  plugin.setSetting("step", 1);
  const page = plugin.page("main");
  assert.equal(page.text("output"), "0");
  page.click("text=Bump");
  page.click("text=Bump");
  assert.equal(page.text("output"), "2");
  assert.deepEqual(page.toasts(), [{ message: "two", tone: "default" }]);
  assert.equal(plugin.storage.get("n/test-project"), 2);
  assert.deepEqual(plugin.events().map(e => e.payload.n), [1, 2]);
  assert.equal(page.text(".project"), "test-project");
});

test("the page cannot reach an rpc it does not list", () => {
  const page = plugin.page("main");
  page.click(".sneaky");
  assert.includes(page.text(".err"), "cannot call");
  assert.deepEqual(page.calls().map(c => c.name), ["hidden"]);
});

test("settings, storage and nodes", () => {
  plugin.setSetting("step", 5);
  assert.equal(plugin.rpc("bump").n, 5);
  assert.equal(plugin.node("read").output.n, 5);
  assert.equal(plugin.rpc("bump", {}, { projectId: "other" }).n, 5);
});

test("each test starts with empty storage", () => {
  assert.equal(plugin.storage.get("n/test-project"), null);
});
`

func writePlugin(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func counterPlugin(t *testing.T) string {
	return writePlugin(t, map[string]string{
		"plugin.json":       testManifest,
		"src/server.ts":     testServer,
		"src/ui/main.tsx":   testPage,
		"src/ui/style.css":  `.counter { display: flex; } .counter output { color: var(--lc-accent); }`,
		"test/main.test.ts": testFile,
	})
}

func TestBuildAndTestAPlugin(t *testing.T) {
	dir := counterPlugin(t)
	res, err := Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]bool{}
	for _, o := range res.Outputs {
		files[o.File] = true
	}
	for _, f := range []string{"dist/server.js", "dist/main.js", "dist/main.css"} {
		if !files[f] {
			t.Fatalf("no %s in %+v", f, res.Outputs)
		}
	}
	css, _ := os.ReadFile(filepath.Join(dir, "dist", "main.css"))
	if !strings.Contains(string(css), "var(--lc-accent)") {
		t.Fatalf("css: %s", css)
	}

	var out bytes.Buffer
	sum, err := RunTests(dir, "", &out)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Passed != 4 || sum.Failed != 0 {
		t.Fatalf("%+v\n%s", sum, out.String())
	}

	// a filter, and a test that fails says why
	sum, _ = RunTests(dir, "nodes", &out)
	if sum.Passed != 1 {
		t.Fatalf("filter: %+v", sum)
	}
	if err := os.WriteFile(filepath.Join(dir, "test", "bad.test.ts"), []byte(`test("wrong", () => { assert.equal(plugin.rpc("bump").n, 99); });`), 0o644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	sum, _ = RunTests(dir, "", &out)
	if sum.Failed != 1 || !strings.Contains(out.String(), "expected 99, got 1") {
		t.Fatalf("%+v\n%s", sum, out.String())
	}
}

// The page's script, as the hub serves it, run with real Preact: what it sends is what the web's
// renderer takes (web/src/lib/pluginSurface.ts).
func TestAPageSendsOperations(t *testing.T) {
	dir := counterPlugin(t)
	if _, err := Build(dir); err != nil {
		t.Fatal(err)
	}
	bundle, err := os.ReadFile(filepath.Join(dir, "dist", "main.js"))
	if err != nil {
		t.Fatal(err)
	}
	vm := goja.New()
	var sent []map[string]any
	var listener goja.Callable
	_ = vm.Set("self", vm.GlobalObject())
	_ = vm.Set("postMessage", func(c goja.FunctionCall) goja.Value {
		raw, _ := json.Marshal(c.Argument(0).Export())
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		sent = append(sent, m)
		return goja.Undefined()
	})
	_ = vm.Set("addEventListener", func(c goja.FunctionCall) goja.Value {
		if c.Argument(0).String() == "message" {
			listener, _ = goja.AssertFunction(c.Argument(1))
		}
		return goja.Undefined()
	})
	if _, err := vm.RunString(`self.queueMicrotask = function (fn) { Promise.resolve().then(fn); };`); err != nil {
		t.Fatal(err)
	}
	script := surface.Script(surface.Context{PluginID: "acme.counter", PageID: "main", ProjectID: "p1"}, bundle)
	if _, err := vm.RunString(string(script)); err != nil {
		t.Fatal(err)
	}

	var ops [][]any
	kinds := map[string]bool{}
	for _, m := range sent {
		kinds[m["t"].(string)] = true
		if m["t"] == "ops" {
			for _, op := range m["ops"].([]any) {
				ops = append(ops, op.([]any))
			}
		}
	}
	if !kinds["ready"] || !kinds["ops"] {
		t.Fatalf("messages: %v", kinds)
	}
	has := func(want ...any) bool {
		for _, op := range ops {
			if len(op) < len(want) {
				continue
			}
			match := true
			for i, w := range want {
				if w != nil && op[i] != w {
					match = false
				}
			}
			if match {
				return true
			}
		}
		return false
	}
	for _, want := range [][]any{
		{"el", nil, "div", float64(0)},
		{"el", nil, "button", float64(0)},
		{"attr", nil, "class", "counter"},
		{"attr", nil, "href", "https://example.com/docs"},
		{"style", nil, "gap", "8px"},
		{"listen", "click"},
		{"text", nil, "p1"},
		{"insert", float64(0), nil, float64(0)},
	} {
		if !has(want...) {
			t.Errorf("no op like %v in %v", want, ops)
		}
	}
	for _, op := range ops {
		if op[0] == "attr" && strings.HasPrefix(op[2].(string), "on") {
			t.Fatalf("a handler went out as an attribute: %v", op)
		}
	}

	// a click from the page reaches the handler, which calls its rpc through the bridge
	button := -1.0
	for _, op := range ops {
		if op[0] == "el" && op[2] == "button" {
			button = op[1].(float64)
			break
		}
	}
	sent = nil
	ev := vm.NewObject()
	_ = ev.Set("data", vm.ToValue(map[string]any{"lucode": 1, "t": "ev", "seq": 1, "target": button, "type": "click", "detail": map[string]any{}}))
	if _, err := listener(goja.Undefined(), ev); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range sent {
		if m["t"] == "rpc" && m["name"] == "bump" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no rpc after a click: %v", sent)
	}
}

func TestBuildErrorsAndLimits(t *testing.T) {
	dir := counterPlugin(t)
	if err := os.WriteFile(filepath.Join(dir, "src", "ui", "main.tsx"), []byte("import 'preact/compat';\nconst x = ;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Build(dir)
	if err == nil || !strings.Contains(err.Error(), "page main does not build") || !strings.Contains(err.Error(), "src/ui/main.tsx:2:") {
		t.Fatalf("a syntax error names the file and line: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "ui", "main.tsx"), []byte("import 'preact/compat';\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(dir); err == nil || !strings.Contains(err.Error(), "preact/compat is not available") {
		t.Fatalf("an import that is not there: %v", err)
	}

	// a stylesheet a page no longer has is not left behind to be served
	_ = os.WriteFile(filepath.Join(dir, "src", "ui", "main.tsx"), []byte(testPage), 0o644)
	if _, err := Build(dir); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "src", "ui", "main.tsx"), []byte(strings.Replace(testPage, `import "./style.css";`, "", 1)), 0o644)
	if _, err := Build(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dist", "main.css")); !os.IsNotExist(err) {
		t.Fatalf("a stale stylesheet: %v", err)
	}

	// the backend cannot import the page's bridge, nor Node's modules
	_ = os.WriteFile(filepath.Join(dir, "src", "server.ts"), []byte(`import fs from "fs"; export const rpc = { a: () => fs };`), 0o644)
	if _, err := Build(dir); err == nil || !strings.Contains(err.Error(), "server does not build") {
		t.Fatalf("a Node module in the backend: %v", err)
	}
}

func TestSourcesStayOutOfTheZip(t *testing.T) {
	m, err := ReadManifest(counterPlugin(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(Sources(m), ","); got != "src" {
		t.Fatalf("sources: %s", got)
	}
}
