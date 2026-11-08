package runner

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"claude-orchestrator/internal/plugins"

	"github.com/nats-io/nats.go"
)

// The runner's agent against a NATS of its own, with no hub: the test signs messages itself, the
// way a hub would, and also the ways a hub would not.

// TestMain turns the test binary into the program exec runs: it starts a child and waits, or just
// says something and leaves.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "helper-echo":
			fmt.Printf("args:%s env:%s pwd:", strings.Join(os.Args[2:], ","), os.Getenv("LUCODE_SECRET_FOR_TEST"))
			wd, _ := os.Getwd()
			fmt.Print(wd)
			return
		case "helper-tree":
			self, _ := os.Executable()
			child := exec.Command(self, "helper-sleep", os.Args[2])
			_ = child.Start()
			_ = os.WriteFile(os.Args[2]+".parent", []byte(strconv.Itoa(os.Getpid())), 0o644)
			time.Sleep(time.Hour)
			return
		case "helper-sleep":
			_ = os.WriteFile(os.Args[2]+".child", []byte(strconv.Itoa(os.Getpid())), 0o644)
			time.Sleep(time.Hour)
			return
		case "helper-loud":
			for i := 0; i < 2000; i++ {
				fmt.Print(strings.Repeat("x", 1000))
			}
			return
		}
	}
	os.Exit(m.Run())
}

func natsServer(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("NATS_SERVER_BIN")
	if bin == "" {
		var err error
		if bin, err = exec.LookPath("nats-server"); err != nil {
			t.Skip("no nats-server binary (set NATS_SERVER_BIN)")
		}
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	cmd := exec.Command(bin, "-a", "127.0.0.1", "-p", strconv.Itoa(port))
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond); err == nil {
			c.Close()
			return fmt.Sprintf("nats://127.0.0.1:%d", port)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("nats-server did not start")
	return ""
}

func zipOf(t *testing.T, files map[string]string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "p.zip")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	for name, body := range files {
		out, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		out.Write([]byte(body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

type rig struct {
	t      *testing.T
	nc     *nats.Conn
	hub    *plugins.Signer
	secret []byte
	agent  *Agent
	id     string
	zip    string
	sha    string
	size   int64
	work   string
	dir    string
	policy string
}

const rigManifest = `{"id":"acme.tool","name":"Tool","version":"1.0.0","sdk":1,"targets":{"runner":{"main":"runner.js","permissions":{
	"fs":"write","git":"read","exec":{"commands":["%s","git"]}}}}}`

const rigCode = `module.exports={rpc:{
	read(a){ return lucode.fs.read(a.path) },
	write(a){ lucode.fs.write(a.path,a.text); return "ok" },
	list(a){ return lucode.fs.list(a.path) },
	exists(a){ return lucode.fs.exists(a.path) },
	stat(a){ return lucode.fs.stat(a.path) },
	run(a){ return lucode.exec.run(a.cmd,a.argv,a.opts) },
	gitStatus(){ return lucode.git.status() },
	gitDiff(a){ return lucode.git.diff(a||{}) },
	gitBranch(){ return lucode.git.branch() },
	gitLog(){ return lucode.git.log({limit:5}) },
	settings(){ return lucode.settings.get("k") },
	whoami(){ return {target:lucode.context.target, plugin:lucode.plugin.id} },
	serverCall(a){ return lucode.server.call(a.name,a.args) },
}}`

func helperName() string {
	self, _ := os.Executable()
	return strings.TrimSuffix(filepath.Base(self), ".exe")
}

// newRig is a runner that an operator has paired with the rig's hub.
func newRig(t *testing.T, policy string) *rig {
	t.Helper()
	r := newUnpairedRig(t, policy)
	r.pair()
	return r
}

func (r *rig) pair() {
	r.t.Helper()
	if err := plugins.SavePairing(r.dir, plugins.Pairing{Runner: r.id, HubKey: r.hub.PublicKey(), Secret: r.secret}); err != nil {
		r.t.Fatal(err)
	}
}

func newUnpairedRig(t *testing.T, policy string) *rig {
	t.Helper()
	url := natsServer(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	secret, err := plugins.NewPairingSecret()
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, nc: nc, hub: plugins.NewSigner(), secret: secret, id: "runner-1", work: t.TempDir(), dir: t.TempDir(), policy: policy}
	r.zip = zipOf(t, map[string]string{
		"plugin.json": fmt.Sprintf(rigManifest, helperName()),
		"runner.js":   rigCode,
	})
	r.sha, _ = plugins.FileSHA256(r.zip)
	info, _ := os.Stat(r.zip)
	r.size = info.Size()
	raw, _ := os.ReadFile(r.zip)
	r.serve(r.sha, raw)
	r.agent, err = New(Options{NATS: nc, RunnerID: r.id, DataDir: r.dir, Policy: policy})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.agent.Start(); err != nil {
		t.Fatal(err)
	}
	return r
}

// serve is what the hub does with a package: sealed chunks, to the runner that holds the secret.
func (r *rig) serve(sha string, raw []byte) {
	r.t.Helper()
	if _, err := r.nc.Subscribe(plugins.PackageSubject(sha), func(m *nats.Msg) {
		var sealed plugins.Sealed
		json.Unmarshal(m.Data, &sealed)
		req, err := plugins.OpenChunkRequest(r.secret, sealed.Runner, sha, sealed.Box)
		if err != nil || req.Offset >= int64(len(raw)) {
			out, _ := json.Marshal(plugins.PackageChunk{})
			m.Respond(out)
			return
		}
		end := req.Offset + plugins.ChunkBytes
		if end > int64(len(raw)) {
			end = int64(len(raw))
		}
		box, _ := plugins.SealChunk(r.secret, sealed.Runner, sha, req.Offset, raw[req.Offset:end])
		out, _ := json.Marshal(plugins.PackageChunk{Box: box})
		m.Respond(out)
	}); err != nil {
		r.t.Fatal(err)
	}
	r.nc.Flush()
}

func (r *rig) sendAs(s *plugins.Signer, kind string, payload any) plugins.RunnerReply {
	r.t.Helper()
	raw, nonce, err := plugins.SealRequest(r.secret, s, kind, r.id, payload)
	if err != nil {
		r.t.Fatal(err)
	}
	return r.open(nonce, r.sendRaw(raw))
}

func (r *rig) sendRaw(raw []byte) []byte {
	r.t.Helper()
	m, err := r.nc.Request(plugins.RunnerSubject(r.id), raw, 30*time.Second)
	if err != nil {
		r.t.Fatal(err)
	}
	return m.Data
}

func (r *rig) open(nonce string, raw []byte) plugins.RunnerReply {
	r.t.Helper()
	rep, err := plugins.OpenReply(r.secret, r.id, nonce, raw)
	if err != nil {
		r.t.Fatalf("%v: %s", err, raw)
	}
	return rep
}

func (r *rig) deliver() plugins.RunnerReply {
	r.t.Helper()
	return r.sendAs(r.hub, plugins.KindDeliver, plugins.DeliverPayload{PluginID: "acme.tool", Version: "1.0.0", SHA256: r.sha, Size: r.size})
}

func allGrants() []string {
	return []string{"runner.fs:read", "runner.fs:write", "runner.git:read", "runner.exec:" + helperName(), "runner.exec:git"}
}

func (r *rig) invokePayload(name string, arg any) plugins.InvokePayload {
	return plugins.InvokePayload{
		ID: fmt.Sprint(time.Now().UnixNano()), PluginID: "acme.tool", SHA256: r.sha, Granted: allGrants(),
		Path: []string{"rpc", name}, Arg: arg, TimeoutMS: 20000, Workspace: r.work,
	}
}

func (r *rig) call(name string, arg any) plugins.RunnerReply {
	r.t.Helper()
	return r.sendAs(r.hub, plugins.KindInvoke, r.invokePayload(name, arg))
}

func (r *rig) mustDeliver() {
	r.t.Helper()
	if rep := r.deliver(); !rep.OK {
		r.t.Fatalf("deliver: %+v", rep)
	}
}

// Until an operator pairs it, a runner takes nothing from anyone, and a first message from a
// stranger makes no difference.
func TestAnUnpairedRunnerRefusesEverything(t *testing.T) {
	r := newUnpairedRig(t, plugins.PolicyStandard)
	for name, kind := range map[string]string{"a delivery": plugins.KindDeliver, "an invocation": plugins.KindInvoke, "an unload": plugins.KindUnload, "a status": plugins.KindStatus} {
		var payload any = plugins.UnloadPayload{PluginID: "acme.tool"}
		if kind == plugins.KindDeliver {
			payload = plugins.DeliverPayload{PluginID: "acme.tool", Version: "1.0.0", SHA256: r.sha, Size: r.size}
		} else if kind == plugins.KindInvoke {
			payload = r.invokePayload("whoami", nil)
		}
		if rep := r.sendAs(r.hub, kind, payload); rep.OK || rep.Code != plugins.RefuseNotPaired {
			t.Errorf("%s: %+v", name, rep)
		}
	}
	if _, err := plugins.LoadPairing(r.dir); err == nil {
		t.Fatal("no message may pair a runner")
	}
	if _, err := os.Stat(filepath.Join(r.dir, "plugins", "acme.tool")); err == nil {
		t.Fatal("nothing should reach its disk")
	}
	// the operator's step takes effect on the running runner
	r.pair()
	r.mustDeliver()
	if rep := r.call("whoami", nil); !rep.OK {
		t.Fatalf("after pairing: %+v", rep)
	}
	// a new secret on the hub revokes the old one at once
	stale := r.secret
	r.secret, _ = plugins.NewPairingSecret()
	r.pair()
	raw, _, err := plugins.SealRequest(stale, r.hub, plugins.KindInvoke, r.id, r.invokePayload("whoami", nil))
	if err != nil {
		t.Fatal(err)
	}
	var s plugins.Sealed
	if err := json.Unmarshal(r.sendRaw(raw), &s); err != nil || s.Code != plugins.RefuseNotPaired || s.Box != "" {
		t.Fatalf("the old secret: %+v %v", s, err)
	}
}

// Every message that is not the paired hub's fresh, signed and sealed word is refused, before any
// code runs.
func TestForeignAndReplayedMessages(t *testing.T) {
	r := newRig(t, plugins.PolicyStandard)
	r.mustDeliver()
	if rep := r.call("whoami", nil); !rep.OK || rep.Value.(map[string]any)["target"] != "runner" {
		t.Fatalf("a good invocation: %+v", rep)
	}
	plainRefusal := func(name string, raw []byte) {
		t.Helper()
		var s plugins.Sealed
		if err := json.Unmarshal(raw, &s); err != nil || s.Box != "" || s.Code != plugins.RefuseNotPaired {
			t.Errorf("%s: %s", name, raw)
		}
	}
	unsigned, _ := json.Marshal(map[string]any{"kind": "invoke", "runner": r.id, "payload": r.invokePayload("whoami", nil)})
	plainRefusal("not sealed", r.sendRaw(unsigned))
	plainRefusal("garbage", r.sendRaw([]byte("not even json")))

	stranger, _ := plugins.NewPairingSecret()
	raw, _, _ := plugins.SealRequest(stranger, plugins.NewSigner(), plugins.KindInvoke, r.id, r.invokePayload("whoami", nil))
	plainRefusal("a stranger's own key", r.sendRaw(raw))
	raw, _, _ = plugins.SealRequest(stranger, plugins.NewSigner(), plugins.KindDeliver, r.id, plugins.DeliverPayload{PluginID: "acme.tool", SHA256: r.sha, Size: r.size})
	plainRefusal("a stranger's delivery", r.sendRaw(raw))
	p, err := plugins.LoadPairing(r.dir)
	if err != nil || p.HubKey != r.hub.PublicKey() {
		t.Fatalf("the pairing changed: %+v %v", p, err)
	}

	// another hub that somehow has the secret still does not have the hub's key
	plainRefusal("the secret but another key", func() []byte {
		raw, _, _ := plugins.SealRequest(r.secret, plugins.NewSigner(), plugins.KindInvoke, r.id, r.invokePayload("whoami", nil))
		return r.sendRaw(raw)
	}())

	// a replay of a good message
	raw, nonce, _ := plugins.SealRequest(r.secret, r.hub, plugins.KindInvoke, r.id, r.invokePayload("whoami", nil))
	first := r.sendRaw(raw)
	if rep := r.open(nonce, first); !rep.OK {
		t.Fatalf("first use: %+v", rep)
	}
	plainRefusal("a replay", r.sendRaw(raw))
	// an old answer does not answer a new question
	_, later, _ := plugins.SealRequest(r.secret, r.hub, plugins.KindInvoke, r.id, r.invokePayload("whoami", nil))
	if _, err := plugins.OpenReply(r.secret, r.id, later, first); err == nil {
		t.Fatal("an old answer opened for a new request")
	}

	// nothing readable crosses the network, in either direction
	var seen [][]byte
	var mu sync.Mutex
	sub, err := r.nc.Subscribe(">", func(m *nats.Msg) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, m.Data)
	})
	if err != nil {
		t.Fatal(err)
	}
	r.nc.Flush()
	inv := r.invokePayload("whoami", map[string]any{"marker": "sniffable-arg"})
	inv.CallSubject, inv.CallToken = "lucode.plugin.hubcall.test", "sniffable-token"
	if rep := r.sendAs(r.hub, plugins.KindInvoke, inv); !rep.OK {
		t.Fatalf("%+v", rep)
	}
	r.nc.Flush()
	time.Sleep(50 * time.Millisecond)
	sub.Unsubscribe()
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("the test saw nothing")
	}
	for _, m := range seen {
		for _, word := range []string{"sniffable", "whoami", "runner.fs", "acme.tool"} {
			if strings.Contains(string(m), word) {
				t.Fatalf("%q readable on the wire: %.200s", word, m)
			}
		}
	}
}

func TestDeliveryChecksThePackage(t *testing.T) {
	r := newRig(t, plugins.PolicyStandard)
	if rep := r.sendAs(r.hub, plugins.KindDeliver, plugins.DeliverPayload{PluginID: "acme.tool", SHA256: strings.Repeat("a", 64), Size: 10}); rep.OK {
		t.Fatalf("a package nobody serves: %+v", rep)
	}
	wrong := plugins.DeliverPayload{PluginID: "acme.other", Version: "1.0.0", SHA256: r.sha, Size: r.size}
	if rep := r.sendAs(r.hub, plugins.KindDeliver, wrong); rep.OK || !strings.Contains(rep.Error, "not a runner plugin of that id") {
		t.Fatalf("a package of another id: %+v", rep)
	}
	big := plugins.DeliverPayload{PluginID: "acme.tool", SHA256: r.sha, Size: plugins.MaxZipBytes + 1}
	if rep := r.sendAs(r.hub, plugins.KindDeliver, big); rep.OK {
		t.Fatalf("a package over the limit: %+v", rep)
	}

	// a hub serving something other than what it signed
	evil := zipOf(t, map[string]string{"plugin.json": fmt.Sprintf(rigManifest, "x"), "runner.js": "module.exports={}"})
	raw, _ := os.ReadFile(evil)
	sha := strings.Repeat("b", 64)
	r.serve(sha, raw)
	if rep := r.sendAs(r.hub, plugins.KindDeliver, plugins.DeliverPayload{PluginID: "acme.tool", SHA256: sha, Size: int64(len(raw))}); rep.OK || !strings.Contains(rep.Error, "not the one the hub signed") {
		t.Fatalf("a swapped package: %+v", rep)
	}

	// chunks from someone without the secret do not count
	foreign := strings.Repeat("c", 64)
	r.nc.Subscribe(plugins.PackageSubject(foreign), func(m *nats.Msg) {
		box, _ := plugins.SealChunk(make([]byte, 32), r.id, foreign, 0, raw)
		out, _ := json.Marshal(plugins.PackageChunk{Box: box})
		m.Respond(out)
	})
	r.nc.Flush()
	if rep := r.sendAs(r.hub, plugins.KindDeliver, plugins.DeliverPayload{PluginID: "acme.tool", SHA256: foreign, Size: int64(len(raw))}); rep.OK || !strings.Contains(rep.Error, "did not come from the hub") {
		t.Fatalf("a chunk from a stranger: %+v", rep)
	}

	// a bad zip with the right hash goes through the same checks as an upload
	bad := zipOf(t, map[string]string{"plugin.json": fmt.Sprintf(rigManifest, "x"), "runner.js": "x", "../evil.js": "x"})
	badRaw, _ := os.ReadFile(bad)
	badSHA, _ := plugins.FileSHA256(bad)
	r.serve(badSHA, badRaw)
	if rep := r.sendAs(r.hub, plugins.KindDeliver, plugins.DeliverPayload{PluginID: "acme.tool", SHA256: badSHA, Size: int64(len(badRaw))}); rep.OK || !strings.Contains(rep.Error, "leaves the package") {
		t.Fatalf("a bad zip: %+v", rep)
	}
	if rep := r.call("whoami", nil); rep.OK {
		t.Fatalf("nothing should have been loaded: %+v", rep)
	}

	// and a good delivery survives a restart of the runner
	r.mustDeliver()
	again, err := New(Options{NATS: r.nc, RunnerID: "runner-1b", DataDir: r.dir, Policy: plugins.PolicyStandard})
	if err != nil {
		t.Fatal(err)
	}
	again.mu.Lock()
	_, ok := again.loaded["acme.tool"]
	again.mu.Unlock()
	if !ok {
		t.Fatal("what was delivered should be there after a restart")
	}

	// unloading removes it
	if rep := r.sendAs(r.hub, plugins.KindUnload, plugins.UnloadPayload{PluginID: "acme.tool"}); !rep.OK {
		t.Fatalf("unload: %+v", rep)
	}
	if rep := r.call("whoami", nil); rep.OK || rep.Code != plugins.RefuseNotDelivered {
		t.Fatalf("after an unload: %+v", rep)
	}
	if _, err := os.Stat(filepath.Join(r.dir, "plugins", "acme.tool")); !os.IsNotExist(err) {
		t.Fatalf("the files should be gone: %v", err)
	}
}

func TestPolicyOffRefusesEverything(t *testing.T) {
	r := newRig(t, plugins.PolicyOff)
	if rep := r.deliver(); rep.OK || rep.Code != plugins.RefusePolicy {
		t.Fatalf("delivery: %+v", rep)
	}
	if rep := r.call("whoami", nil); rep.OK {
		t.Fatalf("invocation: %+v", rep)
	}
	rep := r.sendAs(r.hub, plugins.KindStatus, plugins.UnloadPayload{PluginID: "acme.tool"})
	if !rep.OK || rep.Value.(map[string]any)["policy"] != plugins.PolicyOff {
		t.Fatalf("status: %+v", rep)
	}
	if _, err := New(Options{Policy: "lax", DataDir: t.TempDir()}); err == nil {
		t.Fatal("an unknown policy must not start")
	}
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("this machine cannot make symlinks (%v)", err)
	}
}

func TestFilesystemRules(t *testing.T) {
	r := newRig(t, plugins.PolicyStandard)
	r.mustDeliver()
	write := func(rel, body string) {
		p := filepath.Join(r.work, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/a.txt", "hello")
	write(".env", "SECRET=1")
	write(".env.local", "SECRET=1")
	write("deep/.ENV", "SECRET=1")
	write("server.pem", "x")
	write("keys/id_rsa", "x")
	write("keys/id_rsa.pub", "x")
	write(".lucode-agent", "x")
	write(".git/config", "x")
	write("deep/.git/HEAD", "x")

	if rep := r.call("read", map[string]any{"path": "src/a.txt"}); !rep.OK || rep.Value != "hello" {
		t.Fatalf("a plain read: %+v", rep)
	}
	if rep := r.call("write", map[string]any{"path": "new/dir/b.txt", "text": "hi"}); !rep.OK {
		t.Fatalf("a plain write: %+v", rep)
	}
	if raw, _ := os.ReadFile(filepath.Join(r.work, "new", "dir", "b.txt")); string(raw) != "hi" {
		t.Fatalf("written: %q", raw)
	}
	if rep := r.call("exists", map[string]any{"path": "src/nope"}); !rep.OK || rep.Value != false {
		t.Fatalf("exists: %+v", rep)
	}
	if rep := r.call("stat", map[string]any{"path": "src/a.txt"}); !rep.OK || rep.Value.(map[string]any)["size"] != 5.0 {
		t.Fatalf("stat: %+v", rep)
	}

	refused := []string{
		"../outside.txt", "src/../../outside.txt", "/etc/passwd", `C:\Windows\win.ini`, `..\outside.txt`, "C:/Windows/win.ini",
		".env", ".env.local", "deep/.ENV", "server.pem", "SERVER.PEM", "keys/id_rsa", "keys/id_rsa.pub", ".lucode-agent",
		".git/config", "deep/.git/HEAD", ".GIT/config", ".git", "src/a.txt:stream", ".env.", ".env ", "GIT~1/config", "ENV~1",
		"a\x00b",
	}
	for _, rel := range refused {
		t.Run("read "+rel, func(t *testing.T) {
			if rep := r.call("read", map[string]any{"path": rel}); rep.OK {
				t.Fatalf("read: %+v", rep)
			}
			if rep := r.call("write", map[string]any{"path": rel, "text": "x"}); rep.OK {
				t.Fatalf("write: %+v", rep)
			}
			if rep := r.call("exists", map[string]any{"path": rel}); rep.OK {
				t.Fatalf("exists: %+v", rep)
			}
		})
	}
	rep := r.call("list", map[string]any{"path": "."})
	if !rep.OK {
		t.Fatalf("list: %+v", rep)
	}
	for _, e := range rep.Value.([]any) {
		name := e.(map[string]any)["name"].(string)
		if name == ".env" || name == ".git" || name == "server.pem" || name == ".lucode-agent" {
			t.Fatalf("the name of a secret was listed: %s", name)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(r.work, ".env")); string(raw) != "SECRET=1" {
		t.Fatalf("a refused write reached the disk: %q", raw)
	}
}

func TestSymlinkEscape(t *testing.T) {
	r := newRig(t, plugins.PolicyStandard)
	r.mustDeliver()
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0o644)
	symlinkOrSkip(t, filepath.Join(outside, "secret.txt"), filepath.Join(r.work, "leak.txt"))
	symlinkOrSkip(t, outside, filepath.Join(r.work, "leakdir"))
	os.WriteFile(filepath.Join(r.work, ".env"), []byte("SECRET"), 0o644)
	symlinkOrSkip(t, filepath.Join(r.work, ".env"), filepath.Join(r.work, "notes.txt"))
	os.WriteFile(filepath.Join(r.work, "fine.txt"), []byte("fine"), 0o644)
	symlinkOrSkip(t, filepath.Join(r.work, "fine.txt"), filepath.Join(r.work, "alias.txt"))

	for _, c := range []struct {
		name string
		path string
	}{
		{"a file link out", "leak.txt"},
		{"a folder link out", "leakdir/secret.txt"},
		{"a new file through a folder link", "leakdir/new.txt"},
		{"a link to a secret inside", "notes.txt"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if rep := r.call("read", map[string]any{"path": c.path}); rep.OK {
				t.Fatalf("read: %+v", rep)
			}
			if rep := r.call("write", map[string]any{"path": c.path, "text": "x"}); rep.OK {
				t.Fatalf("write: %+v", rep)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); err == nil {
		t.Fatal("a write went through the link")
	}
	if raw, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(raw) != "outside" {
		t.Fatal("a write changed a file outside")
	}
	if rep := r.call("read", map[string]any{"path": "alias.txt"}); !rep.OK || rep.Value != "fine" {
		t.Fatalf("a link inside the workspace works: %+v", rep)
	}
}

func TestWorkspaceComesFromTheHub(t *testing.T) {
	r := newRig(t, plugins.PolicyFull)
	r.mustDeliver()
	p := r.invokePayload("read", map[string]any{"path": "a.txt"})
	p.Workspace = ""
	for _, name := range []string{"read", "gitStatus", "run"} {
		p.Path = []string{"rpc", name}
		p.Arg = map[string]any{"path": "a", "cmd": helperName(), "argv": []any{"helper-echo"}}
		p.ID = name
		if rep := r.sendAs(r.hub, plugins.KindInvoke, p); rep.OK || !strings.Contains(rep.Error, "no workspace") {
			t.Fatalf("%s with no workspace: %+v", name, rep)
		}
	}
	p.Path = []string{"rpc", "whoami"}
	if rep := r.sendAs(r.hub, plugins.KindInvoke, p); !rep.OK {
		t.Fatalf("the rest of the SDK needs none: %+v", rep)
	}
	p.Workspace = "relative/path"
	if rep := r.sendAs(r.hub, plugins.KindInvoke, p); rep.OK {
		t.Fatalf("a relative workspace: %+v", rep)
	}
	p.Workspace = filepath.VolumeName(r.work) + string(filepath.Separator)
	if rep := r.sendAs(r.hub, plugins.KindInvoke, p); rep.OK {
		t.Fatalf("a whole disk: %+v", rep)
	}
}

func TestGrantsAndPolicy(t *testing.T) {
	r := newRig(t, plugins.PolicyStandard)
	r.mustDeliver()
	os.WriteFile(filepath.Join(r.work, "a.txt"), []byte("x"), 0o644)
	p := r.invokePayload("read", map[string]any{"path": "a.txt"})
	p.Granted = []string{}
	if rep := r.sendAs(r.hub, plugins.KindInvoke, p); rep.OK || !strings.Contains(rep.Error, "fs permission") {
		t.Fatalf("no grant: %+v", rep)
	}
	p = r.invokePayload("write", map[string]any{"path": "a.txt", "text": "y"})
	p.Granted = []string{"runner.fs:read"}
	if rep := r.sendAs(r.hub, plugins.KindInvoke, p); rep.OK || !strings.Contains(rep.Error, "fs write permission") {
		t.Fatalf("read does not allow a write: %+v", rep)
	}
	// exec under standard is refused even with a grant, because a grant cannot exceed the policy
	rep := r.call("run", map[string]any{"cmd": helperName(), "argv": []any{"helper-echo"}})
	if rep.OK || !strings.Contains(rep.Error, "policy") {
		t.Fatalf("exec under standard: %+v", rep)
	}
	if rep := r.call("settings", nil); rep.OK || !strings.Contains(rep.Error, "only works in the server target") {
		t.Fatalf("settings never reach a runner: %+v", rep)
	}
	if rep := r.call("serverCall", map[string]any{"name": "x"}); rep.OK {
		t.Fatalf("no way to the hub without one given: %+v", rep)
	}
}

func TestExec(t *testing.T) {
	self, _ := os.Executable()
	t.Setenv("PATH", filepath.Dir(self)+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("LUCODE_SECRET_FOR_TEST", "topsecret")
	r := newRig(t, plugins.PolicyFull)
	r.mustDeliver()
	name := helperName()

	rep := r.call("run", map[string]any{"cmd": name, "argv": []any{"helper-echo", "a b", "c;rm -rf /", "$(x)"}})
	if !rep.OK {
		t.Fatalf("run: %+v", rep)
	}
	res := rep.Value.(map[string]any)
	out := res["stdout"].(string)
	if res["code"] != 0.0 || !strings.Contains(out, "args:a b,c;rm -rf /,$(x)") {
		t.Fatalf("arguments reach it as they are, with no shell: %+v", res)
	}
	if strings.Contains(out, "topsecret") {
		t.Fatalf("the environment was not scrubbed: %s", out)
	}
	real, _ := filepath.EvalSymlinks(r.work)
	if !strings.Contains(strings.ToLower(out), strings.ToLower(real)) {
		t.Fatalf("it runs in the workspace: %s (want %s)", out, real)
	}

	refused := []map[string]any{
		{"cmd": "powershell"}, {"cmd": "cmd"}, {"cmd": "sh"}, {"cmd": "curl"},
		{"cmd": self}, {"cmd": "../" + name}, {"cmd": `.\` + name}, {"cmd": "./" + name},
		{"cmd": name, "opts": map[string]any{"cwd": "../"}},
		{"cmd": name, "opts": map[string]any{"cwd": ".git"}},
		{"cmd": name, "argv": "not a list"},
		{"cmd": name, "argv": []any{1}},
	}
	for _, c := range refused {
		t.Run(fmt.Sprint(c), func(t *testing.T) {
			if rep := r.call("run", c); rep.OK {
				t.Fatalf("got %+v", rep)
			}
		})
	}

	// output has a cap
	rep = r.call("run", map[string]any{"cmd": name, "argv": []any{"helper-loud"}})
	if !rep.OK {
		t.Fatalf("loud: %+v", rep)
	}
	if res := rep.Value.(map[string]any); res["truncated"] != true || len(res["stdout"].(string)) != maxExecOutput {
		t.Fatalf("loud: truncated=%v len=%d", res["truncated"], len(res["stdout"].(string)))
	}
}

func alive(pid int) bool {
	if runtime.GOOS == "windows" {
		out, _ := exec.Command("tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/NH").Output()
		return strings.Contains(string(out), strconv.Itoa(pid))
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(os.Signal(nil)) == nil
}

// The deadline takes the whole process tree with it, not only the program it started.
func TestExecKillsTheProcessTreeOnDeadline(t *testing.T) {
	self, _ := os.Executable()
	t.Setenv("PATH", filepath.Dir(self)+string(os.PathListSeparator)+os.Getenv("PATH"))
	r := newRig(t, plugins.PolicyFull)
	r.mustDeliver()
	mark := filepath.Join(t.TempDir(), "pids")
	rep := r.call("run", map[string]any{
		"cmd": helperName(), "argv": []any{"helper-tree", mark}, "opts": map[string]any{"timeoutMs": 2500.0},
	})
	if rep.OK || !strings.Contains(rep.Error, "ran out of time") {
		t.Fatalf("got %+v", rep)
	}
	read := func(suffix string) int {
		raw, err := os.ReadFile(mark + suffix)
		if err != nil {
			t.Fatalf("the program did not start: %v", err)
		}
		pid, _ := strconv.Atoi(string(raw))
		return pid
	}
	parent, child := read(".parent"), read(".child")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && (alive(parent) || alive(child)) {
		time.Sleep(200 * time.Millisecond)
	}
	if alive(parent) || alive(child) {
		exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(parent)).Run()
		exec.Command("taskkill", "/F", "/PID", strconv.Itoa(child)).Run()
		t.Fatalf("still alive: parent %v child %v", alive(parent), alive(child))
	}
}

func TestBinaryChecks(t *testing.T) {
	ws := t.TempDir()
	inside := filepath.Join(ws, "tool.exe")
	os.WriteFile(inside, []byte("x"), 0o755)
	if err := checkBinary(inside, ws); err == nil {
		t.Fatal("a program inside the workspace could be planted by the repository")
	}
	outside := filepath.Join(t.TempDir(), "npm.cmd")
	os.WriteFile(outside, []byte("x"), 0o755)
	if err := checkBinary(outside, ws); err == nil || !strings.Contains(err.Error(), "shell") {
		t.Fatalf("a batch file runs through a shell: %v", err)
	}
	for name, want := range map[string]bool{"go": true, "GO.EXE": runtime.GOOS == "windows", "go/x": false, `a\go`: false, "../go": false, "": false, "gofmt": false} {
		if got := commandAllowed(name, []string{"go"}); got != want {
			t.Errorf("commandAllowed(%q) = %v", name, got)
		}
	}
}

func TestGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	r := newRig(t, plugins.PolicyStandard)
	r.mustDeliver()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = r.work
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-b", "main")
	os.WriteFile(filepath.Join(r.work, "a.txt"), []byte("one\n"), 0o644)
	os.WriteFile(filepath.Join(r.work, ".env"), []byte("SECRET=old\n"), 0o644)
	os.WriteFile(filepath.Join(r.work, ".npmrc"), []byte("SECRET=old\n"), 0o644)
	os.MkdirAll(filepath.Join(r.work, ".aws"), 0o755)
	os.WriteFile(filepath.Join(r.work, ".aws", "config"), []byte("SECRET=old\n"), 0o644)
	run("add", "-A")
	run("commit", "-m", "first")
	os.WriteFile(filepath.Join(r.work, "a.txt"), []byte("two\n"), 0o644)
	os.WriteFile(filepath.Join(r.work, ".env"), []byte("SECRET=new\n"), 0o644)
	os.WriteFile(filepath.Join(r.work, ".npmrc"), []byte("SECRET=new\n"), 0o644)
	os.WriteFile(filepath.Join(r.work, ".aws", "config"), []byte("SECRET=new\n"), 0o644)

	if rep := r.call("gitBranch", nil); !rep.OK || rep.Value != "main" {
		t.Fatalf("branch: %+v", rep)
	}
	if rep := r.call("gitStatus", nil); !rep.OK || !strings.Contains(rep.Value.(string), "a.txt") || strings.Contains(rep.Value.(string), ".env") || strings.Contains(rep.Value.(string), ".npmrc") || strings.Contains(rep.Value.(string), ".aws") {
		t.Fatalf("status: %+v", rep)
	}
	if rep := r.call("gitLog", nil); !rep.OK || rep.Value.([]any)[0].(map[string]any)["subject"] != "first" {
		t.Fatalf("log: %+v", rep)
	}
	rep := r.call("gitDiff", nil)
	if !rep.OK || !strings.Contains(rep.Value.(string), "two") {
		t.Fatalf("diff: %+v", rep)
	}
	if strings.Contains(rep.Value.(string), "SECRET") {
		t.Fatalf("a secret file's diff reached the plugin: %s", rep.Value)
	}
	if rep := r.call("gitDiff", map[string]any{"path": ".env"}); rep.OK {
		t.Fatalf("asking for a secret file by name: %+v", rep)
	}
	if rep := r.call("gitDiff", map[string]any{"base": "--output=/tmp/x"}); rep.OK {
		t.Fatalf("an option as a ref: %+v", rep)
	}

	// a repository cannot make a read run something
	marker := filepath.Join(t.TempDir(), "ran")
	hook := fmt.Sprintf("touch %q", marker)
	run("config", "core.fsmonitor", hook)
	run("config", "diff.external", hook)
	if rep := r.call("gitDiff", nil); !rep.OK {
		t.Fatalf("diff with a hostile config: %+v", rep)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the repository's config ran a program")
	}
}

func TestSafeRelPath(t *testing.T) {
	for rel, ok := range map[string]bool{
		"a.txt": true, "src/a/b.go": true, ".": true, "": true, ".gitignore": true, "environment.txt": true,
		"a/../b": true, "a/../../b": false, "..": false, "../..": false, "/a": false, `a\b`: false, "C:a": false,
		".env": false, "x/.env.prod": false, "id_rsa": false, "id_ed25519.pub": false, ".ssh/config": false,
		".git/HEAD": false, "a/.git/x": false, ".lucode": false, ".lucode-x": false, "x.PEM": false, "server.key": false,
	} {
		if _, err := SafeRelPath(rel); (err == nil) != ok {
			t.Errorf("SafeRelPath(%q): %v", rel, err)
		}
	}
}
