package runnercfg

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNormalizeHubAndNATSBase(t *testing.T) {
	for in, want := range map[string]string{
		"lucode.home":            "https://lucode.home",
		"https://lucode.home/":   "https://lucode.home",
		"a.home:8190":            "http://a.home:8190",
		"http://a.home:8190/x/y": "http://a.home:8190",
		" lucode.home ":          "https://lucode.home",
	} {
		got, err := NormalizeHub(in)
		if err != nil || got != want {
			t.Errorf("NormalizeHub(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "ftp://x", "http://"} {
		if _, err := NormalizeHub(bad); err == nil {
			t.Errorf("NormalizeHub(%q) accepted", bad)
		}
	}
	if NATSBase("https://lucode.home") != "wss://lucode.home" || NATSBase("http://a.home:8190") != "ws://a.home:8190" {
		t.Error("NATSBase")
	}
}

func TestProfileURLAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.json")
	c, err := Load(path)
	if err != nil || len(c.Profiles) != 0 {
		t.Fatalf("missing file: %v %v", c, err)
	}
	name, p := c.Ensure("")
	if name != "default" || c.Current != "default" {
		t.Fatalf("name %q current %q", name, c.Current)
	}
	if _, err := p.NATSURL(); err == nil {
		t.Fatal("a profile without a login has a NATS URL")
	}
	p.NATS, p.Token, p.WorkingDir = "wss://lucode.home", "s3cr3t", "/home/x/dev"
	if u, err := p.NATSURL(); err != nil || u != "wss://s3cr3t@lucode.home" {
		t.Fatalf("NATSURL = %q, %v", u, err)
	}
	if err := c.Save(path); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
			t.Errorf("config mode %v, want 0600", st.Mode().Perm())
		}
	}
	c2, err := Load(path)
	if err != nil || c2.Get("").Token != "s3cr3t" || c2.Get("default").WorkingDir != "/home/x/dev" {
		t.Fatalf("reload: %+v %v", c2, err)
	}
	c2.Ensure("work")
	if c2.Current != "work" || c2.Name("") != "work" || c2.Get("default") == nil {
		t.Error("a second profile becomes current and keeps the first")
	}
}

func fakeHub(t *testing.T, enroll int) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Username, Password string }
		json.NewDecoder(r.Body).Decode(&b)
		if b.Username != "marco" || b.Password != "pw" {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "session"})
	})
	mux.HandleFunc("GET /api/runner/enroll", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer session" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if enroll != http.StatusOK {
			http.Error(w, "no nats", enroll)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"token": "natstoken"})
	})
	return httptest.NewServer(mux)
}

func TestLogin(t *testing.T) {
	ts := fakeHub(t, http.StatusOK)
	defer ts.Close()
	if tok, err := Login(context.Background(), ts.URL, "marco", "pw", ""); err != nil || tok != "natstoken" {
		t.Fatalf("login: %q %v", tok, err)
	}
	if _, err := Login(context.Background(), ts.URL, "marco", "bad", ""); err == nil || !strings.Contains(err.Error(), "wrong user or password") {
		t.Fatalf("bad password: %v", err)
	}
}

func TestLoginAgainstAHubWithoutNATS(t *testing.T) {
	ts := fakeHub(t, http.StatusNotFound)
	defer ts.Close()
	if _, err := Login(context.Background(), ts.URL, "marco", "pw", ""); err == nil || !strings.Contains(err.Error(), "--embedded-nats") {
		t.Fatalf("got %v", err)
	}
}

func TestLoginExplainsAnUntrustedCertificate(t *testing.T) {
	ts := httptest.NewTLSServer(http.NotFoundHandler()) // signed by a CA this process does not know
	defer ts.Close()
	_, err := Login(context.Background(), ts.URL, "marco", "pw", "")
	if err == nil || !strings.Contains(err.Error(), "--ca-file") {
		t.Fatalf("got %v", err)
	}
}
