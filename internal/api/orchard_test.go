package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// The proxy against a fake Orchard: no network, no key of the user.
func TestOrchardSettingsAndTranscribe(t *testing.T) {
	var gotAuth, gotLang, gotPrompt, gotFile string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.NotFound(w, r)
			return
		}
		gotAuth = r.Header.Get("Authorization")
		_ = r.ParseMultipartForm(1 << 20)
		gotLang, gotPrompt = r.FormValue("language"), r.FormValue("prompt")
		f, _, err := r.FormFile("file")
		if err == nil {
			buf := make([]byte, 4)
			_, _ = f.Read(buf)
			gotFile = string(buf)
		}
		if r.Header.Get("Authorization") != "Bearer ork_good" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"detail":"invalid key"}`))
			return
		}
		_, _ = w.Write([]byte(`{"text":"  hola mundo  ","language":"es"}`))
	}))
	defer fake.Close()

	oldPath, oldBase := orchardPath, orchardBase
	t.Cleanup(func() { orchardPath, orchardBase = oldPath, oldBase })
	file := filepath.Join(t.TempDir(), "stt-orchard.json")
	orchardPath = func() string { return file }
	orchardBase = func() string { return fake.URL }
	h := &Handler{}

	// no key yet
	rec := httptest.NewRecorder()
	h.orchardTranscribe(rec, httptest.NewRequest("POST", "/", strings.NewReader("RIFFdata")))
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("without a key: %d", rec.Code)
	}

	save := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.putOrchardSettings(rec, httptest.NewRequest("PUT", "/", strings.NewReader(body)))
		return rec
	}
	if rec := save(`{"api_key":"ork_good","language":"ES","prompt":"lucode"}`); rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	// the key never comes back
	rec = httptest.NewRecorder()
	h.getOrchardSettings(rec, httptest.NewRequest("GET", "/", nil))
	if strings.Contains(rec.Body.String(), "ork_good") || !strings.Contains(rec.Body.String(), `"configured":true`) {
		t.Fatalf("settings leak or miss the flag: %s", rec.Body)
	}
	// an empty key keeps the saved one
	if rec := save(`{"language":"es"}`); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"configured":true`) {
		t.Fatalf("keep key: %d %s", rec.Code, rec.Body)
	}
	if rec := save(`{"prompt":"` + strings.Repeat("x", 897) + `"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("long prompt: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.orchardTranscribe(rec, httptest.NewRequest("POST", "/", strings.NewReader("RIFFdata")))
	if rec.Code != 200 {
		t.Fatalf("transcribe: %d %s", rec.Code, rec.Body)
	}
	var out struct{ Text string }
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Text != "hola mundo" || gotAuth != "Bearer ork_good" || gotLang != "es" || gotFile != "RIFF" {
		t.Fatalf("text=%q auth=%q lang=%q prompt=%q file=%q", out.Text, gotAuth, gotLang, gotPrompt, gotFile)
	}

	// a rejected key is reported as such
	save(`{"api_key":"ork_bad"}`)
	rec = httptest.NewRecorder()
	h.orchardTranscribe(rec, httptest.NewRequest("POST", "/", strings.NewReader("RIFFdata")))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "invalid key") {
		t.Fatalf("bad key: %d %s", rec.Code, rec.Body)
	}

	// removing the key
	save(`{"clear_key":true}`)
	rec = httptest.NewRecorder()
	h.getOrchardSettings(rec, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(rec.Body.String(), `"configured":false`) {
		t.Fatalf("clear: %s", rec.Body)
	}
}
