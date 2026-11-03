package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"claude-orchestrator/internal/stt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Voice output. The Fish Audio key is typed in a lucode modal and kept in
// ~/.lucode/tts.json (file mode 0600, plain text: it is as protected as the user's
// profile). It never goes back to the browser: the settings endpoint only says whether
// one is configured. Each request passes the key to the voice worker, which streams
// the audio of the text back as PCM16 mono 44100 Hz.

type ttsSettings struct {
	APIKey  string  `json:"api_key,omitempty"`
	Model   string  `json:"model,omitempty"`
	VoiceID string  `json:"voice_id,omitempty"`
	Speed   float64 `json:"speed,omitempty"`
}

var ttsMu sync.Mutex

func ttsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".lucode", "tts.json")
}

func loadTTS() ttsSettings {
	var s ttsSettings
	if data, err := os.ReadFile(ttsPath()); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	return s
}

func (h *Handler) getTTSSettings(w http.ResponseWriter, r *http.Request) {
	ttsMu.Lock()
	s := loadTTS()
	ttsMu.Unlock()
	jsonResp(w, http.StatusOK, map[string]any{
		"configured": s.APIKey != "", "model": s.Model, "voice_id": s.VoiceID, "speed": s.Speed,
	})
}

// putTTSSettings changes what is sent; an omitted or empty api_key keeps the saved one
// (clear_key removes it).
func (h *Handler) putTTSSettings(w http.ResponseWriter, r *http.Request) {
	var body struct {
		APIKey   string  `json:"api_key"`
		ClearKey bool    `json:"clear_key"`
		Model    string  `json:"model"`
		VoiceID  string  `json:"voice_id"`
		Speed    float64 `json:"speed"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Speed != 0 && (body.Speed < 0.5 || body.Speed > 2) {
		http.Error(w, "speed must be between 0.5 and 2", http.StatusBadRequest)
		return
	}
	ttsMu.Lock()
	defer ttsMu.Unlock()
	s := loadTTS()
	switch {
	case body.ClearKey:
		s.APIKey = ""
	case strings.TrimSpace(body.APIKey) != "":
		s.APIKey = strings.TrimSpace(body.APIKey)
	}
	s.Model, s.VoiceID, s.Speed = strings.TrimSpace(body.Model), strings.TrimSpace(body.VoiceID), body.Speed
	data, _ := json.Marshal(s)
	if err := os.MkdirAll(filepath.Dir(ttsPath()), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(ttsPath(), data, 0o600); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResp(w, http.StatusOK, map[string]any{
		"configured": s.APIKey != "", "model": s.Model, "voice_id": s.VoiceID, "speed": s.Speed,
	})
}

// ttsSpeak streams the audio of {"text": ...} as raw PCM16 mono (X-Sample-Rate gives the rate).
func (h *Handler) ttsSpeak(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
		http.Error(w, "text is required", http.StatusBadRequest)
		return
	}
	ttsMu.Lock()
	s := loadTTS()
	ttsMu.Unlock()
	if s.APIKey == "" {
		http.Error(w, "no Fish Audio key: add it in the voice settings", http.StatusPreconditionFailed)
		return
	}
	c, _, err := stt.Ensure(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer c.Close()

	flusher, _ := w.(http.Flusher)
	started := false
	err = c.Synthesize(r.Context(), stt.TTSRequest{
		Text: body.Text, APIKey: s.APIKey, Model: s.Model, VoiceID: s.VoiceID, Speed: s.Speed,
	}, func(pcm []byte) error {
		if !started {
			started = true
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("X-Sample-Rate", "44100")
			w.Header().Set("Cache-Control", "no-store")
		}
		if _, err := w.Write(pcm); err != nil {
			return err
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})
	if err != nil && !started {
		http.Error(w, ttsErrorText(err), ttsErrorStatus(err))
	}
	// an error after the audio started just ends the stream early
}

func ttsErrorStatus(err error) int {
	switch status.Code(err) {
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.FailedPrecondition:
		return http.StatusPreconditionFailed
	}
	return http.StatusBadGateway
}

func ttsErrorText(err error) string {
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.PermissionDenied:
			return "Fish Audio rejected the key or there is no credit: " + st.Message()
		}
		return st.Message()
	}
	return err.Error()
}
