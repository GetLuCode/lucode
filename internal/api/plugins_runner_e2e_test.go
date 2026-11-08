package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/flows"
	natspkg "claude-orchestrator/internal/natsbus"
	"claude-orchestrator/internal/plugins"
	pluginhub "claude-orchestrator/internal/plugins/hub"
	sessionpkg "claude-orchestrator/internal/session"

	"github.com/nats-io/nats.go"
)

func runIn(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The road to a real lucode-runner, with a temporary NATS and nothing simulated but what would
// spend tokens: claude can only be the recording. The hub signs, the runner pins, fetches and
// checks the package, and a flow node runs on the card's runner with real files.

const runnerManifest = `{
  "id":"acme.rig","name":"Rig","version":"1.0.0","sdk":1,
  "targets":{
    "server":{"main":"server.js","permissions":{"flows":true,"storage":true}},
    "runner":{"main":"runner.js","permissions":{"fs":"write","git":"read","exec":{"commands":["git"]}}}
  },
  "contributes":{
    "settings":[{"key":"token","label":"T","type":"secret"}],
    "flowNodes":[
      {"id":"read","label":"Read a file","target":"runner","fields":[{"name":"path","label":"Path","kind":"template","required":true}],"ports":["next","failed"],"timeoutSeconds":30},
      {"id":"secret","label":"Read a secret","target":"runner","ports":["next","failed"],"timeoutSeconds":30},
      {"id":"call","label":"Call back","target":"runner","ports":["next","failed"],"timeoutSeconds":30},
      {"id":"exec","label":"Exec","target":"runner","ports":["next","failed"],"timeoutSeconds":30}
    ],
    "chatActions":[{"id":"a","label":"A","rpc":"throughServer"}]
  }
}`

const runnerServerCode = `module.exports={rpc:{
  stored(a){ lucode.storage.set("from-runner", a.value); return { token: lucode.settings.get("token"), kept: a.value } },
  throughServer(a){
    var list = lucode.runners.list();
    return lucode.runners.call(list[0].id, "whoami", { x: 1 }, { workspace: { cardId: a.cardId } });
  },
  wrongWorkspace(a){ return lucode.runners.call(lucode.runners.list()[0].id, "whoami", {}, { workspace: "C:/" }) },
}}`

const runnerCode = `module.exports={
  nodes:{
    read(i){ return { output: { text: lucode.fs.read(i.config.path), target: lucode.context.target } } },
    secret(){ return { output: { text: lucode.fs.read(".env") } } },
    call(){ return { output: lucode.server.call("stored", { value: "hello" }) } },
    exec(){ return { output: lucode.exec.run("git", ["--version"]) } },
  },
  rpc:{ whoami(a){ return { target: lucode.context.target, x: a.x, exists: lucode.fs.exists("note.txt") } } },
}`

// builtRunner is the real binary and the recording it may reach, in a temp folder, so tmp/runner
// is not touched.
type builtRunner struct{ bin, fake, fixture string }

func buildRunner(t *testing.T) builtRunner {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "cmd", "lucode-runner", "embedded")); err != nil {
		t.Skip("no embedded hook: run build-runner.sh once")
	}
	dir := t.TempDir()
	b := builtRunner{
		bin: filepath.Join(dir, "lucode-runner.exe"), fake: filepath.Join(dir, "fakeclaude.exe"),
		fixture: filepath.Join(root, "internal", "fakeclaude", "testdata", "two_turns.jsonl"),
	}
	if out, err := runIn(root, "go", "build", "-o", b.bin, "./cmd/lucode-runner"); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	if out, err := runIn(root, "go", "build", "-o", b.fake, "./cmd/fakeclaude"); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return b
}

type realRunner struct {
	cmd  *exec.Cmd
	data string
	log  string
}

func startRunner(t *testing.T, b builtRunner, natsURL, policy string) *realRunner {
	t.Helper()
	data, work := t.TempDir(), t.TempDir()
	logFile := filepath.Join(data, "runner.log")
	out, err := os.Create(logFile)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(b.bin, "--nats="+natsURL, "--data="+data, "--working-dir="+work, "--plugin-policy="+policy)
	// it can only reach the recording: no real claude, no tokens
	cmd.Env = append(os.Environ(), "LUCODE_CLAUDE_BIN="+b.fake, "LUCODE_FAKE_CLAUDE_FIXTURE="+b.fixture)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
		out.Close()
	})
	return &realRunner{cmd: cmd, data: data, log: logFile}
}

func (r *realRunner) id(t *testing.T) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(filepath.Join(r.data, "instance.json"))
		if err == nil {
			var f struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &f) == nil && f.ID != "" {
				return f.ID
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	logs, _ := os.ReadFile(r.log)
	t.Fatalf("the runner did not start:\n%s", logs)
	return ""
}

// pairReal does what an admin and an operator do: the hub makes a code, the real binary takes it.
func pairReal(t *testing.T, r *pluginRig, b builtRunner, runner *realRunner, id string) {
	t.Helper()
	var answer struct{ Code, Command string }
	r.ok("admin", "POST", "/api/plugins/runners/"+id+"/pair", nil, &answer)
	if answer.Command != "lucode-runner pair "+answer.Code {
		t.Fatalf("%+v", answer)
	}
	out, err := exec.Command(b.bin, "pair", "--data="+runner.data, answer.Code).CombinedOutput()
	if err != nil {
		t.Fatalf("pair: %v %s", err, out)
	}
}

func runnerRig(t *testing.T) (*pluginRig, *realRunner, builtRunner, string, string, *nats.Conn) {
	t.Helper()
	url := jetStreamURL(t)
	bin := buildRunner(t)
	runner := startRunner(t, bin, url, plugins.PolicyStandard)
	id := runner.id(t)

	bus := busOn(t, url)
	if err := bus.StartDiscovery(natsInfo("plugin-e2e-hub")); err != nil {
		t.Fatal(err)
	}
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)

	r := newPluginRigWithBus(t, bus, nc)
	work := t.TempDir()
	useRunnerProject(t, r, id, work)
	waitFor(t, "the runner to be discovered", func() bool {
		for _, p := range r.h.pluginPeers() {
			if p.ID == id {
				return p.Policy == plugins.PolicyStandard
			}
		}
		return false
	})
	return r, runner, bin, id, work, nc
}

func rigFiles() map[string]string {
	return map[string]string{"plugin.json": runnerManifest, "server.js": runnerServerCode, "runner.js": runnerCode}
}

func runNode(t *testing.T, r *pluginRig, card dbpkg.KanbanCard, nodeType string, config map[string]any) dbpkg.FlowRun {
	t.Helper()
	f := publishFlow(t, r.h, r.pid, flows.Graph{
		Nodes: []flows.Node{
			{ID: "m", Type: flows.TypeManual},
			{ID: "p", Type: nodeType, Config: config},
		},
		Edges: []flows.Edge{{From: "m", Port: flows.PortEvent, To: "p"}},
	})
	f, _ = dbpkg.GetFlow(r.h.db, f.ID)
	run, err := r.h.flows.StartManual(f, card.ID)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		settleFlows(r)
		got, _ := dbpkg.GetFlowRun(r.h.db, run.ID)
		if got.Status != dbpkg.RunRunning && got.Status != dbpkg.RunQueued {
			return got
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the run did not end")
	return run
}

func stepOutput(t *testing.T, r *pluginRig, run dbpkg.FlowRun, node string) map[string]any {
	t.Helper()
	steps, err := dbpkg.ListFlowSteps(r.h.db, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if s.NodeID == node {
			out := map[string]any{}
			json.Unmarshal(s.Output, &out)
			return out
		}
	}
	return nil
}

func TestPluginOnARealRunner(t *testing.T) {
	r, runner, bin, id, work, nc := runnerRig(t)
	os.WriteFile(filepath.Join(work, "note.txt"), []byte("from the runner's disk"), 0o644)
	os.WriteFile(filepath.Join(work, ".env"), []byte("SECRET=1"), 0o644)

	res := r.install(rigFiles())
	targets := map[string]bool{}
	var execRisk string
	for _, p := range res.Permissions {
		targets[p.Target] = true
		if p.Key == "runner.exec:git" {
			execRisk = p.Risk
		}
	}
	if !targets[plugins.TargetServer] || !targets[plugins.TargetRunner] || execRisk != "high" {
		t.Fatalf("permissions per target: %+v", res.Permissions)
	}

	// nothing happens until someone pairs it: the row says so, and a node fails saying what to do
	var rows []plugins.RunnerRow
	r.ok("user", "GET", "/api/plugins/acme.rig/runners", nil, &rows)
	if len(rows) != 1 || rows[0].Paired || rows[0].Delivered || !strings.Contains(rows[0].Error, "not paired") {
		t.Fatalf("before pairing: %+v", rows)
	}
	card := newCard(t, r.h, r.pid, r.lists["todo"], "work")
	unpaired := runNode(t, r, card, "plugin.acme.rig.read", map[string]any{"path": "note.txt"})
	if unpaired.Status != dbpkg.RunFailed || !strings.Contains(unpaired.Error, "lucode-runner pair") {
		t.Fatalf("unpaired: %s %q", unpaired.Status, unpaired.Error)
	}
	if _, err := os.Stat(filepath.Join(runner.data, "plugins", "acme.rig")); err == nil {
		t.Fatal("nothing should reach an unpaired runner")
	}

	// the operator's step, on the runner as it runs; the hub then delivers
	pairReal(t, r, bin, runner, id)
	waitFor(t, "the delivery", func() bool {
		run := runNode(t, r, card, "plugin.acme.rig.read", map[string]any{"path": "note.txt"})
		return run.Status == dbpkg.RunSucceeded
	})
	r.ok("user", "GET", "/api/plugins/acme.rig/runners", nil, &rows)
	if len(rows) != 1 || !rows[0].Paired || !rows[0].Delivered || !rows[0].Online || rows[0].Policy != plugins.PolicyStandard || rows[0].RunnerID != id {
		t.Fatalf("after pairing: %+v", rows)
	}

	// a node runs on the card's runner, with real files
	run := runNode(t, r, card, "plugin.acme.rig.read", map[string]any{"path": "note.txt"})
	if run.Status != dbpkg.RunSucceeded {
		t.Fatalf("read: %s %q", run.Status, run.Error)
	}
	if out := stepOutput(t, r, run, "p"); out["text"] != "from the runner's disk" || out["target"] != "runner" {
		t.Fatalf("read: %+v", out)
	}

	// the rules hold out there: a secret file, and exec under the default policy
	run = runNode(t, r, card, "plugin.acme.rig.secret", nil)
	if run.Status != dbpkg.RunFailed || !strings.Contains(run.Error, "secret files") {
		t.Fatalf("secret: %s %q", run.Status, run.Error)
	}
	run = runNode(t, r, card, "plugin.acme.rig.exec", nil)
	if run.Status != dbpkg.RunFailed || !strings.Contains(run.Error, "policy") {
		t.Fatalf("exec under standard: %s %q", run.Status, run.Error)
	}

	// lucode.server.call reaches its own plugin's rpc, and a setting only goes if the server passes it
	r.ok("admin", "PUT", "/api/plugins/acme.rig/settings", map[string]any{"token": "abc123secret"}, nil)
	run = runNode(t, r, card, "plugin.acme.rig.call", nil)
	if run.Status != dbpkg.RunSucceeded {
		t.Fatalf("server.call: %s %q", run.Status, run.Error)
	}
	if out := stepOutput(t, r, run, "p"); out["kept"] != "hello" {
		t.Fatalf("server.call: %+v", out)
	}
	if v, ok, _ := dbpkg.PluginStorageGet(r.h.db, "acme.rig", "from-runner"); !ok || v != `"hello"` {
		t.Fatalf("storage: %q %v", v, ok)
	}

	// lucode.runners.call: the server's code calls the runner's
	code, out, raw := r.rpc("acme.rig", "throughServer", map[string]any{"cardId": card.ID})
	if code != 200 || out["target"] != "runner" || out["x"] != 1.0 || out["exists"] != true {
		t.Fatalf("runners.call: %d %s", code, raw)
	}

	// on the bus, without the secret, there is nothing to talk to: not even an answer
	subject := r.h.plugins.(*pluginhub.Host).CallSubjectForTest()
	forgedCall, _ := json.Marshal(plugins.ServerCall{Token: "made-up", RPC: "stored", Args: map[string]any{"value": "forged"}})
	if _, err := nc.Request(subject, forgedCall, 500*time.Millisecond); err == nil {
		t.Fatal("an unsealed call got an answer")
	}
	stranger, _ := plugins.NewPairingSecret()
	sealedByStranger, _, _ := plugins.SealCall(stranger, id, plugins.ServerCall{TS: time.Now().UnixMilli(), Nonce: "n0", Token: "made-up", RPC: "stored"})
	if _, err := nc.Request(subject, sealedByStranger, 500*time.Millisecond); err == nil {
		t.Fatal("a call sealed with another secret got an answer")
	}
	// with the secret, a token nobody issued reaches nothing, and a call cannot be replayed
	secret := dbpkg.PluginRunnerSecret(r.h.db, id)
	call := plugins.ServerCall{TS: time.Now().UnixMilli(), Nonce: "n1", Token: "made-up", RPC: "stored", Args: map[string]any{"value": "forged"}}
	sealedCall, nonce, _ := plugins.SealCall(secret, id, call)
	ask := func() plugins.RunnerReply {
		t.Helper()
		msg, err := nc.Request(subject, sealedCall, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		rep, err := plugins.OpenCallReply(secret, id, nonce, msg.Data)
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}
	if rep := ask(); rep.OK || !strings.Contains(rep.Error, "running invocation") {
		t.Fatalf("a forged call: %+v", rep)
	}
	if rep := ask(); rep.OK || !strings.Contains(rep.Error, "already used") {
		t.Fatalf("a replayed call: %+v", rep)
	}

	// someone else on the bus cannot make the runner run anything, and hears no more than no
	plainNo := func(name string, payload []byte) {
		t.Helper()
		msg, err := nc.Request(plugins.RunnerSubject(id), payload, 5*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		var s plugins.Sealed
		if json.Unmarshal(msg.Data, &s) != nil || s.Box != "" || s.Code != plugins.RefuseNotPaired {
			t.Fatalf("%s: %s", name, msg.Data)
		}
	}
	forged, _ := json.Marshal(map[string]any{"kind": "invoke", "runner": id, "payload": map[string]any{"plugin_id": "acme.rig"}})
	plainNo("unsealed on the bus", forged)
	wrong, _, _ := plugins.SealRequest(stranger, plugins.NewSigner(), plugins.KindInvoke, id, plugins.InvokePayload{ID: "x", PluginID: "acme.rig", Path: []string{"rpc", "whoami"}})
	plainNo("another hub's key", wrong)

	// a new code revokes the runner until it is paired again
	var again struct{ Code string }
	r.ok("admin", "POST", "/api/plugins/runners/"+id+"/pair", nil, &again)
	revoked := runNode(t, r, card, "plugin.acme.rig.read", map[string]any{"path": "note.txt"})
	if revoked.Status != dbpkg.RunFailed || !strings.Contains(revoked.Error, "not paired") {
		t.Fatalf("after a new code: %s %q", revoked.Status, revoked.Error)
	}
	if out, err := exec.Command(bin.bin, "pair", "--data="+runner.data, again.Code).CombinedOutput(); err != nil {
		t.Fatalf("pair: %v %s", err, out)
	}
	// a code for another runner is refused by the command, with the runner's data untouched
	other := t.TempDir()
	if out, err := exec.Command(bin.bin, "pair", "--data="+other, again.Code).CombinedOutput(); err == nil || !strings.Contains(string(out), "this code is for") {
		t.Fatalf("a code for another runner: %v %s", err, out)
	}

	// disabling takes it off the runner, and no invocation is signed for it
	r.ok("admin", "PUT", "/api/plugins/acme.rig/enabled", map[string]any{"enabled": false}, nil)
	waitFor(t, "the unload", func() bool {
		_, err := os.Stat(filepath.Join(runner.data, "plugins", "acme.rig"))
		return os.IsNotExist(err)
	})
	run = runNode(t, r, card, "plugin.acme.rig.read", map[string]any{"path": "note.txt"})
	if run.Status != dbpkg.RunFailed || !strings.Contains(run.Error, "disabled") {
		t.Fatalf("disabled: %s %q", run.Status, run.Error)
	}

	// back on, it is delivered again on demand and works
	r.ok("admin", "PUT", "/api/plugins/acme.rig/enabled", map[string]any{"enabled": true}, nil)
	run = runNode(t, r, card, "plugin.acme.rig.read", map[string]any{"path": "note.txt"})
	if run.Status != dbpkg.RunSucceeded {
		t.Fatalf("enabled again: %s %q", run.Status, run.Error)
	}

	// an offline runner goes to "failed" with a clear message
	runner.cmd.Process.Kill()
	runner.cmd.Wait()
	run = runNode(t, r, card, "plugin.acme.rig.read", map[string]any{"path": "note.txt"})
	if run.Status != dbpkg.RunFailed || !strings.Contains(run.Error, "offline") {
		t.Fatalf("offline: %s %q", run.Status, run.Error)
	}
}

// A runner started with policy off says so, and takes nothing.
func TestRunnerPolicyOffOnARealRunner(t *testing.T) {
	url := jetStreamURL(t)
	bin := buildRunner(t)
	runner := startRunner(t, bin, url, plugins.PolicyOff)
	id := runner.id(t)
	bus := busOn(t, url)
	if err := bus.StartDiscovery(natsInfo("plugin-e2e-hub2")); err != nil {
		t.Fatal(err)
	}
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	r := newPluginRigWithBus(t, bus, nc)
	useRunnerProject(t, r, id, t.TempDir())
	waitFor(t, "the runner", func() bool {
		for _, p := range r.h.pluginPeers() {
			if p.ID == id {
				return p.Policy == plugins.PolicyOff
			}
		}
		return false
	})
	pairReal(t, r, bin, runner, id)
	r.install(rigFiles())
	card := newCard(t, r.h, r.pid, r.lists["todo"], "work")
	run := runNode(t, r, card, "plugin.acme.rig.read", map[string]any{"path": "x"})
	if run.Status != dbpkg.RunFailed || !strings.Contains(run.Error, "refuses plugins") {
		t.Fatalf("%s %q", run.Status, run.Error)
	}
	var rows []plugins.RunnerRow
	r.ok("user", "GET", "/api/plugins/acme.rig/runners", nil, &rows)
	if len(rows) != 1 || !rows[0].Paired || rows[0].Delivered || rows[0].Policy != plugins.PolicyOff {
		t.Fatalf("%+v", rows)
	}
	if _, err := os.Stat(filepath.Join(runner.data, "plugins", "acme.rig")); err == nil {
		t.Fatal("nothing should reach its disk")
	}
}

func natsInfo(id string) natspkg.InstanceInfo {
	return natspkg.InstanceInfo{ID: id, Type: "lucode", Extra: map[string]any{"hub_only": true}}
}

// useRunnerProject gives the rig a project that works on that runner.
func useRunnerProject(t *testing.T, r *pluginRig, runner string, work string) {
	t.Helper()
	proj := dbpkg.Project{ID: "on-runner", Name: "on runner", DefaultModel: "haiku", RunnerID: runner, EnvDirs: map[string][]string{runner: {work}}}
	if err := dbpkg.InsertProject(r.h.db, proj); err != nil {
		t.Fatal(err)
	}
	if err := dbpkg.EnsureDefaultKanbanLists(r.h.db, proj.ID); err != nil {
		t.Fatal(err)
	}
	r.pid = proj.ID
	all, _ := dbpkg.ListKanbanLists(r.h.db, proj.ID)
	for _, l := range all {
		r.lists[l.Kind] = l
	}
}

func newPluginRigWithBus(t *testing.T, bus *natspkg.Bus, nc *nats.Conn) *pluginRig {
	t.Helper()
	h, pid, lists := newTaskTestHandler(t)
	h.bus = bus
	h.manager = sessionpkg.NewManager(h.db, bus, "", "")
	h.flows = flows.NewEngine(h.db, flowHost{h})
	h.flows.TurnEndGrace = 0
	h.SetAuthURL(fakeAuth(t))
	r := &pluginRig{t: t, h: h, pid: pid, lists: lists, browse: &fakeBrowser{}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := h.startPluginHost(ctx, plugins.HubDeps{DataDir: t.TempDir(), NATS: nc}, pluginhub.New); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { flows.SetPluginSource(nil) })
	r.srv = httptest.NewServer(Wrap(h))
	t.Cleanup(r.srv.Close)
	return r
}
