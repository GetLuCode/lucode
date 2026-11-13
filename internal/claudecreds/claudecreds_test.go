package claudecreds

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func writeLogin(t *testing.T, dir string, expiresIn time.Duration) {
	t.Helper()
	body := map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "old-access", "refreshToken": "old-refresh",
		"expiresAt": time.Now().Add(expiresIn).UnixMilli(), "subscriptionType": "max", "scopes": []string{"user:inference"},
	}, "other": "kept"}
	b, _ := json.Marshal(body)
	if err := os.WriteFile(File(dir), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fakeOAuth(t *testing.T, status int, calls *int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		var in map[string]string
		json.NewDecoder(r.Body).Decode(&in)
		if in["grant_type"] != "refresh_token" || in["refresh_token"] != "old-refresh" || in["client_id"] != ClientID {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if status != http.StatusOK {
			http.Error(w, "secret-in-body", status)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "expires_in": 28800})
	}))
}

func TestPassRenewsAnExpiringLoginAndKeepsTheRestOfTheFile(t *testing.T) {
	dir := t.TempDir()
	writeLogin(t, dir, time.Minute)
	var calls int
	srv := fakeOAuth(t, http.StatusOK, &calls)
	defer srv.Close()
	var logs []string
	k := &Keeper{Dirs: func() []string { return []string{dir, dir, "", t.TempDir()} }, TokenURL: srv.URL,
		Logf: func(f string, a ...any) { logs = append(logs, f) }}

	wait := k.Pass(context.Background())
	if calls != 1 {
		t.Fatalf("token endpoint called %d times, want 1 (a repeated dir counts once)", calls)
	}
	c, err := Read(dir)
	if err != nil || c.AccessToken != "new-access" || c.RefreshToken != "new-refresh" || c.TTL() < 7*time.Hour {
		t.Fatalf("after renewal: %+v %v", c, err)
	}
	raw, _ := os.ReadFile(File(dir))
	for _, want := range []string{`"other": "kept"`, `"subscriptionType": "max"`, "user:inference"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("the file lost %s:\n%s", want, raw)
		}
	}
	if wait != time.Hour { // 8 h left minus the margin, capped at an hour
		t.Errorf("next pass in %v, want 1h", wait)
	}
	if len(logs) != 2 { // "watching" the first time the dir is seen, then "renewed"
		t.Errorf("logs: %v", logs)
	}
}

func TestPassLeavesAHealthyLoginAlone(t *testing.T) {
	dir := t.TempDir()
	writeLogin(t, dir, 30*time.Minute)
	var calls int
	srv := fakeOAuth(t, http.StatusOK, &calls)
	defer srv.Close()
	k := &Keeper{Dirs: func() []string { return []string{dir} }, TokenURL: srv.URL}
	wait := k.Pass(context.Background())
	if calls != 0 {
		t.Fatal("renewed a token with 30 minutes left")
	}
	if wait < 24*time.Minute || wait > 26*time.Minute {
		t.Errorf("next pass in %v, want about 25m", wait)
	}
}

func TestAFailedRenewalKeepsTheFileAndDoesNotLeakTheBody(t *testing.T) {
	dir := t.TempDir()
	writeLogin(t, dir, time.Minute)
	var calls int
	srv := fakeOAuth(t, http.StatusBadRequest, &calls)
	defer srv.Close()
	var logged string
	k := &Keeper{Dirs: func() []string { return []string{dir} }, TokenURL: srv.URL,
		Logf: func(f string, a ...any) { logged += f }}
	k.Pass(context.Background())
	if c, _ := Read(dir); c.AccessToken != "old-access" || c.RefreshToken != "old-refresh" {
		t.Fatalf("a failed renewal changed the file: %+v", c)
	}
	if strings.Contains(logged, "secret-in-body") || !strings.Contains(logged, "claude-login") {
		t.Errorf("log: %q", logged)
	}
}

func TestNoDirsOrNoLoginWaitsAndDoesNothing(t *testing.T) {
	k := &Keeper{Dirs: func() []string { return []string{t.TempDir()} }, TokenURL: "http://127.0.0.1:1"}
	if wait := k.Pass(context.Background()); wait != 10*time.Minute {
		t.Errorf("wait %v, want 10m", wait)
	}
}
