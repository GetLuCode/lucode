package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Second speech-to-text engine: Orchard Run (https://orchardrun.com), a cloud service that
// transcribes a whole recording. The web records while the user holds the dictation (push to
// talk: click the mic, speak, click again) and posts the WAV here; the hub forwards it to
// Orchard's synchronous endpoint. The API key is typed in the voice settings and kept in
// ~/.lucode/stt-orchard.json (mode 0600, like the Fish Audio key); it never goes back to the browser.

type orchardSettings struct {
	APIKey   string `json:"api_key,omitempty"`
	Language string `json:"language,omitempty"` // ISO 639-1; empty = auto-detect
	Prompt   string `json:"prompt,omitempty"`   // up to 896 characters of context (names, jargon)
}

const (
	orchardMaxPrompt = 896
	orchardMaxAudio  = 100 << 20 // 100 MB of WAV is about 50 minutes at 16 kHz mono
)

var (
	orchardMu   sync.Mutex
	orchardPath = func() string {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".lucode", "stt-orchard.json")
	}
	// LUCODE_ORCHARD_URL points tests (or a self-hosted Orchard) somewhere else.
	orchardBase = func() string {
		if v := strings.TrimRight(os.Getenv("LUCODE_ORCHARD_URL"), "/"); v != "" {
			return v
		}
		return "https://api.orchardrun.com"
	}
	orchardClient = &http.Client{Timeout: 10 * time.Minute} // Orchard's sync limit is 600 s
)

func loadOrchard() orchardSettings {
	var s orchardSettings
	if data, err := os.ReadFile(orchardPath()); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s
}

func orchardView(s orchardSettings) map[string]any {
	return map[string]any{"configured": s.APIKey != "", "language": s.Language, "prompt": s.Prompt}
}

func (h *Handler) getOrchardSettings(w http.ResponseWriter, r *http.Request) {
	orchardMu.Lock()
	s := loadOrchard()
	orchardMu.Unlock()
	jsonResp(w, http.StatusOK, orchardView(s))
}

// putOrchardSettings: an omitted or empty api_key keeps the saved one (clear_key removes it).
func (h *Handler) putOrchardSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		APIKey   string `json:"api_key"`
		ClearKey bool   `json:"clear_key"`
		Language string `json:"language"`
		Prompt   string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	lang := strings.ToLower(strings.TrimSpace(body.Language))
	if len(lang) > 8 {
		http.Error(w, "language must be an ISO 639-1 code such as es or en", http.StatusBadRequest)
		return
	}
	prompt := strings.TrimSpace(body.Prompt)
	if len([]rune(prompt)) > orchardMaxPrompt {
		http.Error(w, fmt.Sprintf("prompt is limited to %d characters", orchardMaxPrompt), http.StatusBadRequest)
		return
	}
	orchardMu.Lock()
	defer orchardMu.Unlock()
	s := loadOrchard()
	switch {
	case body.ClearKey:
		s.APIKey = ""
	case strings.TrimSpace(body.APIKey) != "":
		s.APIKey = strings.TrimSpace(body.APIKey)
	}
	s.Language, s.Prompt = lang, prompt
	data, _ := json.Marshal(s)
	if err := os.MkdirAll(filepath.Dir(orchardPath()), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(orchardPath(), data, 0o600); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, orchardView(s))
}

// orchardTranscribe takes the recording as the raw request body (a WAV) and answers {"text": ...}.
func (h *Handler) orchardTranscribe(w http.ResponseWriter, r *http.Request) {
	orchardMu.Lock()
	s := loadOrchard()
	orchardMu.Unlock()
	if s.APIKey == "" {
		http.Error(w, "no Orchard key: add it in the voice settings", http.StatusPreconditionFailed)
		return
	}
	audio, err := io.ReadAll(http.MaxBytesReader(w, r.Body, orchardMaxAudio))
	if err != nil {
		http.Error(w, "could not read the audio: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(audio) == 0 {
		http.Error(w, "empty audio", http.StatusBadRequest)
		return
	}

	var form bytes.Buffer
	mw := multipart.NewWriter(&form)
	part, _ := mw.CreateFormFile("file", "dictation.wav")
	_, _ = part.Write(audio)
	if s.Language != "" {
		_ = mw.WriteField("language", s.Language)
	}
	if s.Prompt != "" {
		_ = mw.WriteField("prompt", s.Prompt)
	}
	_ = mw.WriteField("response_format", "json")
	_ = mw.Close()

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, orchardBase()+"/v1/audio/transcriptions", &form)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+s.APIKey)
	resp, err := orchardClient.Do(req)
	if err != nil {
		http.Error(w, "could not reach Orchard: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		http.Error(w, orchardErrorText(resp.StatusCode, data), orchardErrorStatus(resp.StatusCode))
		return
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		http.Error(w, "Orchard answered something that is not JSON", http.StatusBadGateway)
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{"text": strings.TrimSpace(out.Text)})
}

func orchardErrorStatus(code int) int {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return http.StatusForbidden
	case http.StatusTooManyRequests:
		return http.StatusTooManyRequests
	case http.StatusRequestEntityTooLarge, http.StatusBadRequest:
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

// orchardErrorText reads Orchard's {"detail": "..."} error.
func orchardErrorText(code int, body []byte) string {
	var e struct {
		Detail any `json:"detail"`
	}
	detail := ""
	if json.Unmarshal(body, &e) == nil && e.Detail != nil {
		if s, ok := e.Detail.(string); ok {
			detail = s
		} else if b, err := json.Marshal(e.Detail); err == nil {
			detail = string(b)
		}
	}
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return "Orchard rejected the key or there is no credit: " + detail
	case http.StatusTooManyRequests:
		return "Orchard rate limit reached, try again in a moment: " + detail
	}
	if detail == "" {
		detail = strings.TrimSpace(string(body))
	}
	return fmt.Sprintf("Orchard error %d: %s", code, detail)
}
