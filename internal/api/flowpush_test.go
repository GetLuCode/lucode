package api

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	dbpkg "claude-orchestrator/internal/db"
	"claude-orchestrator/internal/flows"
)

// fakePushService is a push service plus the browser behind it. It checks what a real one
// checks (the VAPID signature and its audience) and decrypts with the subscriber's private key
// as RFC 8291 says, so a test passes only if a browser could read the message. Nothing here
// reaches a real push service or the user's keys.
type fakePushService struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	status int
	device map[string]fakeDevice
	got    []map[string]string
	header []http.Header
}

type fakeDevice struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func useFakePush(t *testing.T) *fakePushService {
	t.Helper()
	dir := t.TempDir()
	prevPath, prevClient := flowPushPath, flowPushClient
	flowPushPath = func() string { return filepath.Join(dir, "flows-push.json") }
	t.Setenv("LUCODE_PUSH_CONTACT", "")
	p := &fakePushService{t: t, status: http.StatusCreated, device: map[string]fakeDevice{}}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.status >= 300 {
			w.WriteHeader(p.status)
			return
		}
		body, _ := io.ReadAll(r.Body)
		p.checkVAPID(r)
		dev, ok := p.device[r.URL.Path]
		if !ok {
			t.Errorf("post to unknown device %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var msg map[string]string
		if err := json.Unmarshal(decryptPush(t, dev, body), &msg); err != nil {
			t.Errorf("decrypted text is not JSON: %v", err)
		}
		p.got = append(p.got, msg)
		p.header = append(p.header, r.Header.Clone())
		w.WriteHeader(p.status)
	}))
	client := p.srv.Client()
	client.CheckRedirect = flowPushClient.CheckRedirect
	flowPushClient = client
	t.Cleanup(func() {
		p.srv.Close()
		flowPushPath, flowPushClient = prevPath, prevClient
	})
	return p
}

// subscribe makes a browser and returns the subscription it would send to the hub.
func (p *fakePushService) subscribe(name string) dbpkg.PushSubscription {
	p.t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		p.t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	p.mu.Lock()
	p.device["/"+name] = fakeDevice{priv: priv, auth: auth}
	p.mu.Unlock()
	return dbpkg.PushSubscription{
		Endpoint: p.srv.URL + "/" + name,
		P256dh:   base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(auth),
	}
}

func (p *fakePushService) messages() []map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]map[string]string{}, p.got...)
}

func (p *fakePushService) setStatus(s int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.status = s
}

// checkVAPID verifies the Authorization header the way a push service does.
func (p *fakePushService) checkVAPID(r *http.Request) {
	t := p.t
	auth, ok := strings.CutPrefix(r.Header.Get("Authorization"), "vapid t=")
	if !ok {
		t.Errorf("Authorization: %q", r.Header.Get("Authorization"))
		return
	}
	token, k, ok := strings.Cut(auth, ", k=")
	if !ok {
		t.Errorf("no k= in %q", auth)
		return
	}
	keys, err := flowPushKeys()
	if err != nil {
		t.Error(err)
		return
	}
	if k != keys.Public {
		t.Errorf("k=%s is not the hub's public key", k)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Errorf("JWT: %q", token)
		return
	}
	raw, _ := base64.RawURLEncoding.DecodeString(k)
	x, y := elliptic.Unmarshal(elliptic.P256(), raw)
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if x == nil || len(sig) != 64 || !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, sum[:],
		new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("the VAPID signature does not verify")
	}
	claimsRaw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Aud string `json:"aud"`
		Sub string `json:"sub"`
	}
	json.Unmarshal(claimsRaw, &claims)
	if claims.Aud != p.srv.URL {
		t.Errorf("aud %q, want %q", claims.Aud, p.srv.URL)
	}
	if claims.Sub != "mailto:"+pushDefaultContact {
		t.Errorf("sub %q", claims.Sub)
	}
}

// decryptPush is what the browser does with an aes128gcm record (RFC 8188 and RFC 8291).
func decryptPush(t *testing.T, dev fakeDevice, body []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("record of %d bytes", len(body))
	}
	salt, idLen := body[:16], int(body[20])
	if rs := binary.BigEndian.Uint32(body[16:20]); rs < 18 || int(rs) < len(body)-21-idLen {
		t.Fatalf("record size %d for %d bytes", rs, len(body))
	}
	senderKey := body[21 : 21+idLen]
	cipherText := body[21+idLen:]
	sender, err := ecdh.P256().NewPublicKey(senderKey)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := dev.priv.ECDH(sender)
	if err != nil {
		t.Fatal(err)
	}
	info := append(append([]byte("WebPush: info\x00"), dev.priv.PublicKey().Bytes()...), senderKey...)
	ikm, err := hkdf.Key(sha256.New, shared, dev.auth, string(info), 32)
	if err != nil {
		t.Fatal(err)
	}
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, cipherText, nil)
	if err != nil {
		t.Fatalf("the device cannot decrypt it: %v", err)
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		t.Fatal("no last-record delimiter")
	}
	return plain[:len(plain)-1]
}

func pushAPI(t *testing.T, h *Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	h.registerFlows(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

func subscriptionJSON(s dbpkg.PushSubscription) string {
	data, _ := json.Marshal(map[string]any{"endpoint": s.Endpoint, "keys": map[string]string{"p256dh": s.P256dh, "auth": s.Auth}})
	return string(data)
}

// The whole way: a published flow, the engine, the real library, and a device that reads it.
func TestPushNodeReachesADeviceThatCanReadIt(t *testing.T) {
	h, pid, _, _ := flowHarness(t)
	push := useFakePush(t)
	if rec := pushAPI(t, h, "PUT", "/api/flow-push/subscription", subscriptionJSON(push.subscribe("phone"))); rec.Code != http.StatusNoContent {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body)
	}

	f := publishFlow(t, h, pid, flows.Graph{
		Nodes: []flows.Node{
			{ID: "t", Type: flows.TypeManual},
			{ID: "p", Type: flows.TypePushNotification, Config: map[string]any{"title": "Título", "body": "Run {{run.depth}} ñandú"}},
		},
		Edges: []flows.Edge{{From: "t", Port: flows.PortEvent, To: "p"}},
	})
	f, err := dbpkg.GetFlow(h.db, f.ID) // with its published version
	if err != nil {
		t.Fatal(err)
	}
	run, err := h.flows.StartManual(f, "")
	if err != nil {
		t.Fatal(err)
	}
	h.flows.Drain()
	h.flows.WaitForEvaluations()
	h.flows.Drain()

	run, err = dbpkg.GetFlowRun(h.db, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != dbpkg.RunSucceeded {
		t.Fatalf("run: %s %s", run.Status, run.Error)
	}
	got := push.messages()
	if len(got) != 1 {
		t.Fatalf("messages: %+v", got)
	}
	m := got[0]
	if m["title"] != "Título" || m["body"] != "Run 0 ñandú" {
		t.Fatalf("text: %+v", m)
	}
	if want := "/projects/" + pid + "/flows?flow=" + f.ID + "&run=" + run.ID; m["url"] != want {
		t.Fatalf("url %q, want %q", m["url"], want)
	}
	if !strings.HasPrefix(m["tag"], "flow-step-") {
		t.Fatalf("tag %q", m["tag"])
	}
	head := push.header[0]
	if head.Get("Urgency") != "high" || head.Get("TTL") == "" || head.Get("TTL") == "0" || head.Get("Content-Encoding") != "aes128gcm" {
		t.Fatalf("headers: %v", head)
	}
}

func TestPushWithoutDevicesSaysHowToGetOne(t *testing.T) {
	h, _, _, _ := flowHarness(t)
	useFakePush(t)
	_, err := flowHost{h}.SendPush(context.Background(), flows.PushMessage{Title: "t", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "turn them on in the Flows tab") {
		t.Fatalf("err: %v", err)
	}
}

func TestPushForgetsADeviceThatNoLongerExists(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		h, _, _, _ := flowHarness(t)
		push := useFakePush(t)
		sub := push.subscribe("old")
		if err := dbpkg.SavePushSubscription(h.db, sub); err != nil {
			t.Fatal(err)
		}
		push.setStatus(status)
		res, err := flowHost{h}.SendPush(context.Background(), flows.PushMessage{Title: "t", Body: "b"})
		if err == nil || res.Removed != 1 {
			t.Fatalf("%d: res %+v err %v", status, res, err)
		}
		if list, _ := dbpkg.ListPushSubscriptions(h.db); len(list) != 0 {
			t.Fatalf("%d: still there: %+v", status, list)
		}
	}
}

// A service that fails for a while must not lose the device, and the secret URL stays out of
// the error, which is shown in the Runs tab.
func TestPushFailureKeepsTheDeviceAndHidesTheEndpoint(t *testing.T) {
	h, _, _, _ := flowHarness(t)
	push := useFakePush(t)
	sub := push.subscribe("phone")
	if err := dbpkg.SavePushSubscription(h.db, sub); err != nil {
		t.Fatal(err)
	}
	push.setStatus(http.StatusInternalServerError)
	if _, err := (flowHost{h}).SendPush(context.Background(), flows.PushMessage{Title: "t", Body: "b"}); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err: %v", err)
	}
	if list, _ := dbpkg.ListPushSubscriptions(h.db); len(list) != 1 {
		t.Fatalf("device lost: %+v", list)
	}

	push.srv.Close() // nobody listening
	_, err := flowHost{h}.SendPush(context.Background(), flows.PushMessage{Title: "t", Body: "b"})
	if err == nil {
		t.Fatal("no error with the service down")
	}
	if strings.Contains(err.Error(), "/phone") || strings.Contains(err.Error(), sub.Endpoint) {
		t.Fatalf("the error shows the endpoint: %v", err)
	}
}

func TestPushReachingOneDeviceIsSuccess(t *testing.T) {
	h, _, _, _ := flowHarness(t)
	push := useFakePush(t)
	if err := dbpkg.SavePushSubscription(h.db, push.subscribe("phone")); err != nil {
		t.Fatal(err)
	}
	dead := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer dead.Close()
	flowPushClient.Transport.(*http.Transport).TLSClientConfig.RootCAs.AddCert(dead.Certificate())
	sub := push.subscribe("laptop")
	sub.Endpoint = dead.URL + "/laptop"
	if err := dbpkg.SavePushSubscription(h.db, sub); err != nil {
		t.Fatal(err)
	}
	res, err := flowHost{h}.SendPush(context.Background(), flows.PushMessage{Title: "t", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 1 || res.Failed != 1 {
		t.Fatalf("result: %+v", res)
	}
}

func TestPushPayloadAlwaysFits(t *testing.T) {
	for name, body := range map[string]string{
		"long":    strings.Repeat("a", 20000),
		"quotes":  strings.Repeat(`"`, 5000),
		"control": strings.Repeat("\x01", 5000),
		"accents": strings.Repeat("ñ", 5000),
		"emoji":   strings.Repeat("🚀", 5000),
	} {
		data, err := pushPayload(flows.PushMessage{Title: strings.Repeat("é", 500), Body: body, ProjectID: "p", FlowID: "f", RunID: "r", Seq: 1}, "/icon-192.abc.png")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(data) > pushMaxPayload {
			t.Fatalf("%s: %d bytes", name, len(data))
		}
		var m map[string]string
		if err := json.Unmarshal(data, &m); err != nil || m["body"] == "" {
			t.Fatalf("%s: %v %v", name, err, m)
		}
		if strings.ContainsRune(m["body"], '�') || strings.ContainsRune(m["title"], '�') {
			t.Fatalf("%s: a character was cut in half", name)
		}
	}
}

func TestPushAPINeverGivesThePrivateKeyAndKeepsItsKeys(t *testing.T) {
	h, _, _, _ := flowHarness(t)
	useFakePush(t)
	first := pushAPI(t, h, "GET", "/api/flow-push", "")
	if first.Code != http.StatusOK {
		t.Fatalf("%d %s", first.Code, first.Body)
	}
	keys, err := flowPushKeys()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first.Body.String(), keys.Private) || strings.Contains(first.Body.String(), "private") {
		t.Fatalf("private key sent: %s", first.Body)
	}
	if !strings.Contains(first.Body.String(), keys.Public) {
		t.Fatalf("no public key: %s", first.Body)
	}
	if second := pushAPI(t, h, "GET", "/api/flow-push", ""); second.Body.String() != first.Body.String() {
		t.Fatal("the keys changed between calls: every device would be cut off")
	}
}

func TestPushDamagedKeyFileIsNotReplaced(t *testing.T) {
	h, _, _, _ := flowHarness(t)
	useFakePush(t)
	if err := os.WriteFile(flowPushPath(), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if rec := pushAPI(t, h, "GET", "/api/flow-push", ""); rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "damaged") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if data, _ := os.ReadFile(flowPushPath()); string(data) != "not json" {
		t.Fatalf("file replaced: %s", data)
	}
}

func TestPushRejectsSubscriptionsThatCouldNotWork(t *testing.T) {
	h, _, _, _ := flowHarness(t)
	push := useFakePush(t)
	good := push.subscribe("phone")
	cases := map[string]func(*dbpkg.PushSubscription){
		"plain http":    func(s *dbpkg.PushSubscription) { s.Endpoint = "http://example.com/x" },
		"not a url":     func(s *dbpkg.PushSubscription) { s.Endpoint = "::" },
		"local file":    func(s *dbpkg.PushSubscription) { s.Endpoint = "file:///etc/passwd" },
		"huge endpoint": func(s *dbpkg.PushSubscription) { s.Endpoint = "https://example.com/" + strings.Repeat("a", 3000) },
		"key off curve": func(s *dbpkg.PushSubscription) {
			s.P256dh = base64.RawURLEncoding.EncodeToString(append([]byte{4}, make([]byte, 64)...))
		},
		"short auth": func(s *dbpkg.PushSubscription) { s.Auth = "AAAA" },
		"no keys":    func(s *dbpkg.PushSubscription) { s.P256dh, s.Auth = "", "" },
	}
	for name, spoil := range cases {
		bad := good
		spoil(&bad)
		if rec := pushAPI(t, h, "PUT", "/api/flow-push/subscription", subscriptionJSON(bad)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if list, _ := dbpkg.ListPushSubscriptions(h.db); len(list) != 0 {
		t.Fatalf("stored: %+v", list)
	}
}

func TestPushSubscribeTestAndUnsubscribe(t *testing.T) {
	h, _, _, _ := flowHarness(t)
	push := useFakePush(t)
	sub := push.subscribe("phone")
	endpoint, _ := json.Marshal(map[string]string{"endpoint": sub.Endpoint})

	if rec := pushAPI(t, h, "POST", "/api/flow-push/test", string(endpoint)); rec.Code != http.StatusNotFound {
		t.Fatalf("a test to an unknown device: %d", rec.Code)
	}
	for range 2 { // subscribing twice is one device
		if rec := pushAPI(t, h, "PUT", "/api/flow-push/subscription", subscriptionJSON(sub)); rec.Code != http.StatusNoContent {
			t.Fatalf("subscribe: %d %s", rec.Code, rec.Body)
		}
	}
	if list, _ := dbpkg.ListPushSubscriptions(h.db); len(list) != 1 {
		t.Fatalf("devices: %+v", list)
	}
	if rec := pushAPI(t, h, "POST", "/api/flow-push/test", string(endpoint)); rec.Code != http.StatusNoContent {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
	if got := push.messages(); len(got) != 1 || got[0]["url"] != "/" {
		t.Fatalf("messages: %+v", got)
	}
	if rec := pushAPI(t, h, "DELETE", "/api/flow-push/subscription", string(endpoint)); rec.Code != http.StatusNoContent {
		t.Fatalf("unsubscribe: %d %s", rec.Code, rec.Body)
	}
	if list, _ := dbpkg.ListPushSubscriptions(h.db); len(list) != 0 {
		t.Fatalf("devices: %+v", list)
	}
}
