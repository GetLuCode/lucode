package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type zipEntry struct {
	name string
	body string
	mode os.FileMode
}

const zipManifest = `{"id":"acme.hello","name":"Hello","version":"1.0.0","sdk":1,"main":"main.js"}`

func writeZip(t *testing.T, entries []zipEntry) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "p.zip")
	f, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w := zip.NewWriter(f)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		if e.mode != 0 {
			h.SetMode(e.mode)
		}
		out, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := out.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return file
}

func good(extra ...zipEntry) []zipEntry {
	return append([]zipEntry{{name: "plugin.json", body: zipManifest}, {name: "main.js", body: "module.exports={}"}}, extra...)
}

func TestZipIsRefused(t *testing.T) {
	cases := []struct {
		name    string
		entries []zipEntry
		want    string
	}{
		{"parent path", good(zipEntry{name: "../evil.js"}), "leaves the package"},
		{"nested parent", good(zipEntry{name: "a/../../evil.js"}), "leaves the package"},
		{"absolute", good(zipEntry{name: "/etc/evil.js"}), "absolute"},
		{"backslash", good(zipEntry{name: `a\evil.js`}), "backslash"},
		{"drive letter", good(zipEntry{name: "C:/evil.js"}), "colon"},
		{"stream", good(zipEntry{name: "main.js:ads"}), "colon"},
		{"NUL", good(zipEntry{name: "a\x00.js"}), "NUL"},
		{"reserved name", good(zipEntry{name: "con.txt"}), "reserves"},
		{"reserved in a folder", good(zipEntry{name: "data/LPT1/a.txt"}), "reserves"},
		{"trailing dot", good(zipEntry{name: "main.js."}), "dot or a space"},
		{"symlink", good(zipEntry{name: "link.txt", body: "/etc/passwd", mode: os.ModeSymlink | 0o777}), "link"},
		{"case duplicate", good(zipEntry{name: "MAIN.JS", body: "x"}), "twice"},
		{"exact duplicate", good(zipEntry{name: "data/a.txt", body: "x"}, zipEntry{name: "data/a.txt", body: "y"}), "twice"},
		{"file and folder", good(zipEntry{name: "data", body: "x"}, zipEntry{name: "data/a.txt", body: "y"}), ""},
		{"extension", good(zipEntry{name: "run.exe", body: "x"}), "not allowed"},
		{"no extension", good(zipEntry{name: "Makefile", body: "x"}), "not allowed"},
		{"script in assets", good(zipEntry{name: "assets/a.js", body: "x"}), "images"},
		{"no manifest", []zipEntry{{name: "main.js", body: "x"}}, "plugin.json"},
		{"manifest not at root", []zipEntry{{name: "a/plugin.json", body: zipManifest}}, "plugin.json"},
		{"main missing", []zipEntry{{name: "plugin.json", body: zipManifest}}, "main.js"},
		{"ratio", good(zipEntry{name: "data/big.txt", body: strings.Repeat("a", 5<<20)}), "compresses too well"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := CheckZip(writeZip(t, c.entries), ParseOptions{})
			if err == nil {
				t.Fatal("expected a refusal")
			}
			if c.want != "" && !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

func TestZipLimits(t *testing.T) {
	big := make([]zipEntry, 0, MaxZipFiles+1)
	for i := 0; i <= MaxZipFiles; i++ {
		big = append(big, zipEntry{name: fmt.Sprintf("data/f%d.txt", i), body: "x"})
	}
	if _, err := CheckZip(writeZip(t, good(big...)), ParseOptions{}); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("file count: %v", err)
	}
	// these cannot be stored for real: the sizes come from the headers
	huge := filepath.Join(t.TempDir(), "huge.zip")
	if err := os.WriteFile(huge, bytes.Repeat([]byte{0}, MaxZipBytes+1), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckZip(huge, ParseOptions{}); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("zip size: %v", err)
	}
	for _, c := range []struct {
		name string
		size int
		want string
	}{{"one file over", MaxFileBytes + 1, "larger than"}} {
		t.Run(c.name, func(t *testing.T) {
			// random-looking data so the ratio check does not answer first
			body := make([]byte, c.size)
			x := uint32(1)
			for i := range body {
				x = x*1664525 + 1013904223
				body[i] = byte(x >> 24)
			}
			_, err := CheckZip(writeZip(t, good(zipEntry{name: "data/big.txt", body: string(body)})), ParseOptions{})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestExtractIsAtomic(t *testing.T) {
	file := writeZip(t, good(zipEntry{name: "data/a.txt", body: "hi"}))
	if _, err := CheckZip(file, ParseOptions{}); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	dest := filepath.Join(base, "pkg")
	if err := ExtractZip(file, dest); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(dest, "data", "a.txt")); err != nil || string(raw) != "hi" {
		t.Fatalf("%q %v", raw, err)
	}
	// a destination already there is not overwritten and leaves no temporary folder behind
	if err := ExtractZip(file, dest); err == nil {
		t.Fatal("expected a refusal: the destination exists")
	}
	entries, _ := os.ReadDir(base)
	if len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
	// nothing outside the package is reachable through it
	pkg := &Package{Dir: dest}
	for _, bad := range []string{"../x", "/etc/passwd", `..\x`, "data/../../x"} {
		if _, err := pkg.ReadFile(bad, 10); err == nil {
			t.Errorf("ReadFile(%q) should fail", bad)
		}
	}
}

func TestManifestRules(t *testing.T) {
	base := func(rest string) string {
		return `{"id":"acme.hello","name":"H","version":"1.0.0","sdk":1,` + rest + `}`
	}
	cases := []struct {
		name string
		raw  string
		want string
		opts ParseOptions
	}{
		{"reserved id", `{"id":"lucode.x","name":"H","version":"1.0.0","sdk":1,"main":"m.js"}`, "reserved", ParseOptions{}},
		{"bad id", `{"id":"Acme","name":"H","version":"1.0.0","sdk":1,"main":"m.js"}`, "look like", ParseOptions{}},
		{"bad version", `{"id":"acme.x","name":"H","version":"1","sdk":1,"main":"m.js"}`, "semver", ParseOptions{}},
		{"sdk", `{"id":"acme.x","name":"H","version":"1.0.0","sdk":9,"main":"m.js"}`, "sdk", ParseOptions{}},
		{"no code", base(`"description":"x"`), "no code", ParseOptions{}},
		{"unknown permission", base(`"main":"m.js","permissions":{"root":true}`), "root", ParseOptions{}},
		{"exec on the server", base(`"main":"m.js","permissions":{"exec":{"commands":["go"]}}`), "runner permissions", ParseOptions{}},
		{"storage on the runner", base(`"targets":{"runner":{"main":"r.js","permissions":{"storage":true}}}`), "server permissions", ParseOptions{}},
		{"exec with a path", base(`"targets":{"runner":{"main":"r.js","permissions":{"exec":{"commands":["/bin/sh"]}}}}`), "base names", ParseOptions{}},
		{"bare star host", base(`"main":"m.js","permissions":{"http":{"hosts":["*"]}}`), "host name", ParseOptions{}},
		{"ip as a host", base(`"main":"m.js","permissions":{"http":{"hosts":["127.0.0.1"]}}`), "host name", ParseOptions{}},
		{"http origin", base(`"main":"m.js","permissions":{"browser":{"origins":["http://a.example"]}}`), "https origin", ParseOptions{}},
		{"claude model id", base(`"main":"m.js","contributes":{"models":[{"id":"claude-opus-5-5","label":"x"}]}`), "reserved", ParseOptions{}},
		{"sonnet model id", base(`"main":"m.js","contributes":{"models":[{"id":"sonnet","label":"x"}]}`), "reserved", ParseOptions{}},
		{"model id claiming another plugin", base(`"main":"m.js","contributes":{"models":[{"id":"plugin:other.x/y","label":"x"}]}`), "may only use", ParseOptions{}},
		{"model on runner only", base(`"targets":{"runner":{"main":"r.js"}},"contributes":{"models":[{"id":"m","label":"x"}]}`), "server target", ParseOptions{}},
		{"node without flows", base(`"main":"m.js","contributes":{"flowNodes":[{"id":"n","label":"x"}]}`), "flows permission", ParseOptions{}},
		{"node on a missing target", base(`"main":"m.js","permissions":{"flows":true},"contributes":{"flowNodes":[{"id":"n","label":"x","target":"runner"}]}`), "no runner target", ParseOptions{}},
		{"node with a dot", base(`"main":"m.js","permissions":{"flows":true},"contributes":{"flowNodes":[{"id":"a.b","label":"x"}]}`), "may only use", ParseOptions{}},
		{"secret setting on runner only", base(`"targets":{"runner":{"main":"r.js"}},"contributes":{"settings":[{"key":"k","label":"K","type":"secret"}]}`), "never reach", ParseOptions{}},
		{"too long a timeout", base(`"main":"m.js","contributes":{"models":[{"id":"m","label":"x","timeoutSeconds":99999}]}`), "at most", ParseOptions{}},
		{"main outside", base(`"main":"../m.js"`), ".js file in the zip", ParseOptions{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(c.raw), c.opts)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
	if _, err := ParseManifest([]byte(`{"id":"lucode.x","name":"H","version":"1.0.0","sdk":1,"main":"m.js"}`), ParseOptions{Bundled: true}); err != nil {
		t.Fatalf("a bundled plugin may use lucode.*: %v", err)
	}
	m, err := ParseManifest([]byte(`{"id":"acme.x","name":"H","version":"1.0.0","sdk":1,"main":"m.js","permissions":{"storage":true},"contributes":{"models":[{"id":"m","label":"M"}]}}`), ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if m.Targets.Server == nil || m.Targets.Server.Main != "m.js" || !m.Targets.Server.Permissions.Storage {
		t.Fatalf("the old form means the server target: %+v", m.Targets)
	}
}

func TestGrantsAndWireModels(t *testing.T) {
	m, err := ParseManifest([]byte(`{"id":"acme.x","name":"H","version":"1.0.0","sdk":1,"targets":{
		"server":{"main":"s.js","permissions":{"board":"write","http":{"hosts":["a.example.com","b.example.com"]}}},
		"runner":{"main":"r.js","permissions":{"fs":"write","exec":{"commands":["go"]}}}}}`), ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	risks := map[string]PermissionInfo{}
	for _, p := range PermissionsOf(m) {
		risks[p.Key] = p
	}
	if risks["runner.exec:go"].Risk != "high" || risks["runner.exec:go"].Target != TargetRunner {
		t.Fatalf("exec is high risk on the runner: %+v", risks["runner.exec:go"])
	}
	got := Effective(TargetServer, m.Targets.Server.Permissions, []string{"server.board:read", "server.http:a.example.com", "runner.fs:read"})
	if got.Board != "read" || len(got.HTTP.Hosts) != 1 || got.HTTP.Hosts[0] != "a.example.com" {
		t.Fatalf("server: %+v", got)
	}
	rg := Effective(TargetRunner, m.Targets.Runner.Permissions, []string{"runner.fs:read", "server.board:write"})
	if rg.FS != "read" || rg.Exec != nil {
		t.Fatalf("a grant is per target: %+v", rg)
	}

	if WireModel("acme.x", "m") != "plugin:acme.x/m" {
		t.Fatal(WireModel("acme.x", "m"))
	}
	for s, ok := range map[string]bool{"plugin:acme.x/m": true, "plugin:acme.x": false, "plugin:/m": false, "plugin:Bad/m": false, "plugin:a.b/x/y": false, "sonnet": false} {
		if _, _, got := ParseWireModel(s); got != ok {
			t.Errorf("ParseWireModel(%q) = %v", s, got)
		}
	}
}

// ---- http ----

func TestPublicIP(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "1.1.1.1": true, "2606:4700::1111": true,
		"127.0.0.1": false, "::1": false, "10.0.0.5": false, "192.168.1.5": false, "172.16.0.1": false,
		"169.254.169.254": false, "100.64.0.1": false, "0.0.0.0": false, "::": false, "fe80::1": false, "fc00::1": false,
		"::ffff:127.0.0.1": false, "::ffff:169.254.169.254": false, "224.0.0.1": false, "198.18.0.1": false,
	} {
		if got := PublicIP(netip.MustParseAddr(addr)); got != want {
			t.Errorf("%s: got %v", addr, got)
		}
	}
}

// httpTest runs a real TLS server, reached through a fake public address.
func httpTest(t *testing.T, handler http.Handler, resolve map[string][]string) (*httptest.Server, *httpCaller) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	return srv, newHTTPCaller(HTTPOptions{
		TLS: &tls.Config{InsecureSkipVerify: true},
		Resolve: func(_ context.Context, host string) ([]netip.Addr, error) {
			var out []netip.Addr
			for _, a := range resolve[host] {
				out = append(out, netip.MustParseAddr(a))
			}
			if out == nil {
				return nil, fmt.Errorf("no such host")
			}
			return out, nil
		},
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, srv.Listener.Addr().String())
		},
	})
}

func TestFetchSSRF(t *testing.T) {
	var sawCookie, sawHost string
	mux := http.NewServeMux()
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		sawCookie, sawHost = r.Header.Get("Cookie"), r.Host
		http.SetCookie(w, &http.Cookie{Name: "c", Value: "1"})
		io.WriteString(w, `{"hello":"world"}`)
	})
	mux.HandleFunc("/to-other", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://other.example.com/ok", http.StatusFound)
	})
	mux.HandleFunc("/to-plain", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://a.example.com/ok", http.StatusFound)
	})
	mux.HandleFunc("/to-private", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://inner.example.com/ok", http.StatusFound)
	})
	mux.HandleFunc("/to-allowed", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://b.example.com/ok", http.StatusFound)
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, _ *http.Request) {
		w.Write(bytes.Repeat([]byte("x"), maxResponseBytes+10))
	})
	_, c := httpTest(t, mux, map[string][]string{
		"a.example.com":     {"93.184.216.34"},
		"b.example.com":     {"93.184.216.35"},
		"other.example.com": {"93.184.216.36"},
		"inner.example.com": {"192.168.1.5"},
		"meta.example.com":  {"169.254.169.254"},
		"lo.example.com":    {"127.0.0.1"},
		"mix.example.com":   {"10.0.0.1", "::1"},
	})
	hosts := []string{"a.example.com", "b.example.com", "inner.example.com", "meta.example.com", "lo.example.com", "mix.example.com", "*.wild.example.com"}
	get := func(url string) (map[string]any, error) {
		return c.fetch(context.Background(), FetchRequest{URL: url, Hosts: hosts, Headers: map[string]string{"Cookie": "secret=1", "Host": "evil.example.com", "X-Ok": "y"}})
	}
	res, err := get("https://a.example.com/ok")
	if err != nil {
		t.Fatal(err)
	}
	if res["status"] != 200.0 || res["text"] != `{"hello":"world"}` || res["json"].(map[string]any)["hello"] != "world" {
		t.Fatalf("%+v", res)
	}
	if sawCookie != "" || sawHost != "a.example.com" {
		t.Fatalf("cookie %q and host %q must not be set by a plugin", sawCookie, sawHost)
	}
	if res, err := get("https://a.example.com/to-allowed"); err != nil || res["status"] != 200.0 {
		t.Fatalf("a redirect to an allowed host: %v %v", res, err)
	}
	for _, c := range []struct{ name, url, want string }{
		{"plain http", "http://a.example.com/ok", "https"},
		{"host not allowed", "https://evil.example.com/ok", "not in this plugin"},
		{"literal private ip", "https://192.168.1.5/ok", "not in this plugin"},
		{"literal loopback", "https://127.0.0.1/ok", "not in this plugin"},
		{"literal metadata", "https://169.254.169.254/latest", "not in this plugin"},
		{"user in the address", "https://x:y@a.example.com/ok", "user"},
		{"resolves to private", "https://inner.example.com/ok", "cannot reach"},
		{"resolves to metadata", "https://meta.example.com/ok", "cannot reach"},
		{"resolves to loopback", "https://lo.example.com/ok", "cannot reach"},
		{"every answer private", "https://mix.example.com/ok", "cannot reach"},
		{"redirect to another host", "https://a.example.com/to-other", "not in this plugin"},
		{"redirect to plain http", "https://a.example.com/to-plain", "https"},
		{"redirect to a private name", "https://a.example.com/to-private", "cannot reach"},
		{"response too large", "https://a.example.com/big", "larger than"},
		{"wildcard does not match the apex", "https://wild.example.com/ok", "not in this plugin"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := get(c.url); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}

func TestFetchNeedsThePermission(t *testing.T) {
	r := runner(t, `module.exports={rpc:{go(){ return lucode.http.fetch("https://a.example.com/") }}}`)
	_, err := call(t, r, 5*time.Second, nil)
	if err == nil || !strings.Contains(err.Error(), "http permission") {
		t.Fatalf("got %v", err)
	}
}

// ---- signing ----

func TestVerifier(t *testing.T) {
	hub := NewSigner()
	dir := t.TempDir()
	secret, err := NewPairingSecret()
	if err != nil {
		t.Fatal(err)
	}
	v := NewVerifier("r1", dir)
	send := func(key []byte, s *Signer, kind, runner string) ([]byte, error) {
		raw, _, err := SealRequest(key, s, kind, runner, map[string]any{"a": 1})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = v.Open(raw)
		return raw, err
	}
	code := func(err error) string {
		if r, ok := err.(*Refusal); ok {
			return r.Code
		}
		return fmt.Sprint(err)
	}
	// no pairing: nothing gets in, and no stranger's first message makes one
	for _, kind := range []string{KindDeliver, KindInvoke, KindStatus} {
		if _, err := send(secret, hub, kind, "r1"); code(err) != RefuseNotPaired {
			t.Fatalf("%s before pairing: %v", kind, err)
		}
	}
	if _, err := LoadPairing(dir); err == nil {
		t.Fatal("a delivery must not pair a runner")
	}

	// the code carries the pairing, and only the runner it names takes it
	c, err := PairingCode(Pairing{Runner: "r1", HubKey: hub.PublicKey(), Secret: secret})
	if err != nil {
		t.Fatal(err)
	}
	p, err := ParsePairingCode(c)
	if err != nil || p.Runner != "r1" || p.HubKey != hub.PublicKey() || !bytes.Equal(p.Secret, secret) {
		t.Fatalf("%+v %v", p, err)
	}
	for _, bad := range []string{"", "nope", pairingCodePrefix + "!!!", pairingCodePrefix + "e30"} {
		if _, err := ParsePairingCode(bad); err == nil {
			t.Errorf("code %q", bad)
		}
	}
	if err := SavePairing(dir, p); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(PairingFile(dir)); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("%v %v", info, err)
	}
	if _, _, err := NewVerifier("r2", dir).Open([]byte("{}")); code(err) != RefuseNotPaired {
		t.Fatalf("a pairing for another runner: %v", err)
	}

	raw, err := send(secret, hub, KindInvoke, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := v.Open(raw); code(err) != RefuseReplay {
		t.Fatalf("a replay: %v", err)
	}
	other, _ := NewPairingSecret()
	if _, err := send(other, hub, KindInvoke, "r1"); code(err) != RefuseNotPaired {
		t.Fatalf("sealed with another secret: %v", err)
	}
	if _, err := send(secret, NewSigner(), KindInvoke, "r1"); code(err) != RefuseBadSignature {
		t.Fatalf("the secret without the hub's key: %v", err)
	}
	if _, err := send(secret, hub, KindInvoke, "r2"); code(err) != RefuseBadSignature {
		t.Fatalf("meant for another runner: %v", err)
	}
	// too old, whatever the nonce
	v3 := NewVerifier("r1", dir)
	v3.SetClock(func() time.Time { return time.Now().Add(EnvelopeWindow + time.Minute) })
	if _, _, err := v3.Open(raw); code(err) != RefuseReplay {
		t.Fatalf("too old: %v", err)
	}

	// what is inside the box cannot be changed, because the signature covers it
	for name, mutate := range map[string]func(*Envelope){
		"no signature":    func(e *Envelope) { e.Sig = "" },
		"changed payload": func(e *Envelope) { e.Payload = json.RawMessage(`{"a":2}`) },
		"changed kind":    func(e *Envelope) { e.Kind = KindUnload },
		"changed time":    func(e *Envelope) { e.TS++ },
		"changed nonce":   func(e *Envelope) { e.Nonce = "x" },
	} {
		t.Run(name, func(t *testing.T) {
			e, _ := hub.Sign(KindInvoke, "r1", map[string]any{"a": 1})
			mutate(&e)
			inner, _ := json.Marshal(e)
			box, _ := Seal(secret, sealHubToRunner, "r1", "", inner)
			raw, _ := json.Marshal(Sealed{Runner: "r1", Box: box})
			if _, _, err := v.Open(raw); code(err) != RefuseBadSignature {
				t.Fatalf("got %v", err)
			}
		})
	}
	if _, _, err := v.Open([]byte(`{"kind":"invoke","runner":"r1","payload":{}}`)); code(err) != RefuseBadSignature {
		t.Fatalf("an unsealed message: %v", err)
	}
	a, _ := LoadOrCreateSigner(dir)
	b, _ := LoadOrCreateSigner(dir)
	if a.PublicKey() != b.PublicKey() {
		t.Fatal("the hub's key must be stable")
	}
}

// A sealed message says nothing on the wire and only opens for what it was sealed for.
func TestSealing(t *testing.T) {
	secret, _ := NewPairingSecret()
	other, _ := NewPairingSecret()
	reply := RunnerReply{OK: true, Value: "secret-value"}
	raw, err := SealReply(secret, "r1", "nonce-1", reply)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("secret-value")) {
		t.Fatalf("readable on the wire: %s", raw)
	}
	if got, err := OpenReply(secret, "r1", "nonce-1", raw); err != nil || got.Value != "secret-value" {
		t.Fatalf("%+v %v", got, err)
	}
	var s Sealed
	json.Unmarshal(raw, &s)
	flipped, _ := base64.StdEncoding.DecodeString(s.Box)
	flipped[len(flipped)-1] ^= 1
	s.Box = base64.StdEncoding.EncodeToString(flipped)
	tampered, _ := json.Marshal(s)
	for name, try := range map[string]func() (RunnerReply, error){
		"a tampered reply":                 func() (RunnerReply, error) { return OpenReply(secret, "r1", "nonce-1", tampered) },
		"a reply from another runner":      func() (RunnerReply, error) { return OpenReply(secret, "r2", "nonce-1", raw) },
		"a replay against a later request": func() (RunnerReply, error) { return OpenReply(secret, "r1", "nonce-2", raw) },
		"another secret":                   func() (RunnerReply, error) { return OpenReply(other, "r1", "nonce-1", raw) },
		"a reply that is not sealed": func() (RunnerReply, error) {
			forged, _ := json.Marshal(RunnerReply{OK: true, Value: "forged"})
			return OpenReply(secret, "r1", "nonce-1", forged)
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := try(); err == nil {
				t.Fatalf("opened: %+v", got)
			}
		})
	}
	// a plain answer can only say no
	for _, c := range []Sealed{{Code: RefuseNotPaired, Error: "x"}, {Code: RefusePolicy, Error: "x"}} {
		plain, _ := json.Marshal(c)
		got, err := OpenReply(secret, "r1", "n", plain)
		if err != nil || got.OK || got.Value != nil || got.Code != c.Code {
			t.Fatalf("%+v %v", got, err)
		}
	}
	// a direction cannot be turned around
	box, _ := Seal(secret, sealHubToRunner, "r1", "", []byte("x"))
	if _, err := Unseal(secret, sealRunnerToHub, "r1", "", box); err == nil {
		t.Fatal("opened in the other direction")
	}
	// parts of a package cannot be moved around
	chunk, _ := SealChunk(secret, "r1", "sha", 0, []byte("x"))
	if _, err := OpenChunk(secret, "r1", "sha", 512, chunk); err == nil {
		t.Fatal("a chunk opened at another offset")
	}
}
