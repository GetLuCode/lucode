package api

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"claude-orchestrator/internal/stt"

	"github.com/gorilla/websocket"
)

// Voice input. The browser records the microphone and streams it here; lucode relays
// it to the voice worker (Parakeet, int8 model) over gRPC and sends the transcripts
// back. Protocol of GET /ws/stt:
//
//	client -> {"type":"start","sample_rate":48000,"wake":true,"wake_backend":"vosk","wake_phrase":"hey veronica",
//	           "silence_seconds":0.6,"stable_seconds":2,"stable_repeats":10}   text, first message (wake* and the phrase-end options are optional)
//	client -> PCM16 mono little-endian               binary, ~40 ms per frame
//	client -> {"type":"flush"}                       closes the phrase in progress
//	server -> {"type":"loading"|"ready"|"speech"|"wake"|"wake_timeout"|"realtime"|"final"|"error", "text":...}
//	          {"type":"wake_phrase","ok":bool,"text":...}   result of validating the wake phrase

// sttStableSeconds is how long the partial text must stay unchanged for the worker to
// close the phrase on its own (worker option stable_text_seconds, 0.3-5). OFF by default:
// measured with the int8 CPU model it cut phrases while they were still being spoken
// (the partials lag behind the audio) and the finals got worse at 1, 2 and 3 s. It may
// suit the GPU model. LUCODE_STT_STABLE_SECONDS=<seconds> turns it on.
func sttStableSeconds() float64 {
	if v, err := strconv.ParseFloat(os.Getenv("LUCODE_STT_STABLE_SECONDS"), 64); err == nil {
		if v == 0 {
			return 0
		}
		return math.Min(5, math.Max(0.3, v))
	}
	return 0
}

// clampSilence is the audio silence that closes a phrase (worker silence_seconds, 0.2-5);
// 0 leaves the worker default (0.6 s).
func clampSilence(v *float64) float64 {
	if v == nil || math.IsNaN(*v) {
		return 0
	}
	return math.Min(5, math.Max(0.2, *v))
}

// stableSecondsOr is the page's stable_seconds (0 = off, else 0.3-5) or, when it sends none,
// the LUCODE_STT_STABLE_SECONDS default.
func stableSecondsOr(v *float64) float64 {
	if v == nil || math.IsNaN(*v) {
		return sttStableSeconds()
	}
	if *v <= 0 {
		return 0
	}
	return math.Min(5, math.Max(0.3, *v))
}

// clampRepeats is stable_repeats (0 = off, up to 1000); nil keeps the worker default.
func clampRepeats(v *int) *int {
	if v == nil {
		return nil
	}
	n := min(1000, max(0, *v))
	return &n
}

// sttEndpointIdle is how long the partial text may stay unchanged before lucode closes
// the phrase itself (see stt.Endpointer). 2 s by default; LUCODE_STT_ENDPOINT_SECONDS
// changes it, 0 turns it off.
func sttEndpointIdle() time.Duration {
	if v, err := strconv.ParseFloat(os.Getenv("LUCODE_STT_ENDPOINT_SECONDS"), 64); err == nil {
		return time.Duration(math.Max(0, v) * float64(time.Second))
	}
	return 2 * time.Second
}

// sttStatus reports whether the voice worker is up and has its model loaded.
func (h *Handler) sttStatus(w http.ResponseWriter, r *http.Request) {
	c, info, ok := stt.Running(r.Context())
	if !ok {
		jsonResp(w, http.StatusOK, map[string]any{"state": "off"})
		return
	}
	defer c.Close()
	state := "loading"
	if info.Ready {
		state = "ready"
	}
	jsonResp(w, http.StatusOK, map[string]any{"state": state, "engine": info.EngineKey, "listening": info.Listening})
}

// sttStart launches the worker (if needed) without waiting for its model.
func (h *Handler) sttStart(w http.ResponseWriter, r *http.Request) {
	c, info, err := stt.Ensure(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	c.Close()
	state := "loading"
	if info.Ready {
		state = "ready"
	}
	jsonResp(w, http.StatusOK, map[string]any{"state": state, "engine": info.EngineKey})
}

// sttModels lists the voice models, the one chosen in the voice settings and what the shared
// worker is running right now (null when none answers).
func (h *Handler) sttModels(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"models": stt.Models, "selected": stt.Selected(), "worker": nil}
	if c, info, ok := stt.Running(r.Context()); ok {
		c.Close()
		resp["worker"] = map[string]any{"model": info.EngineKey, "ready": info.Ready, "listening": info.Listening}
	}
	jsonResp(w, http.StatusOK, resp)
}

// sttSetModel chooses the voice model: it is saved, and a worker running another model is
// replaced (the page asks for this explicitly). It returns once the new worker answers; the
// model may still be loading, which /api/stt/models then reports as ready=false.
func (h *Handler) sttSetModel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Model string `json:"model"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || body.Model == "" {
		http.Error(w, "expected {\"model\": \"<id>\"}", http.StatusBadRequest)
		return
	}
	if !stt.KnownModel(body.Model) {
		http.Error(w, "unknown voice model "+body.Model, http.StatusBadRequest)
		return
	}
	info, err := stt.Switch(r.Context(), body.Model)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	resp := map[string]any{"selected": stt.Selected(), "worker": nil} // no worker: it starts when somebody needs it
	if info.EngineKey != "" {
		resp["worker"] = map[string]any{"model": info.EngineKey, "ready": info.Ready, "listening": info.Listening}
	}
	jsonResp(w, http.StatusOK, resp)
}

// sttVoskModel serves the Vosk Spanish model as a .tar.gz for the browser's wasm hotword
// detector. The browser keeps it in its HTTP cache (ServeFile sends Last-Modified).
func (h *Handler) sttVoskModel(w http.ResponseWriter, r *http.Request) {
	path, err := stt.VoskModelArchive()
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Cache-Control", "no-cache") // revalidate each time; 304 when unchanged
	http.ServeFile(w, r, path)
}

func (h *Handler) sttSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	var wmu sync.Mutex
	send := func(v map[string]any) {
		wmu.Lock()
		defer wmu.Unlock()
		_ = conn.WriteJSON(v)
	}

	// first message: the sample rate the browser captures at
	var start struct {
		Type        string `json:"type"`
		SampleRate  int    `json:"sample_rate"`
		Wake        bool   `json:"wake"`
		WakeBackend string `json:"wake_backend"`
		WakePhrase  string `json:"wake_phrase"`
		// How the worker decides a phrase is over; each one that is absent keeps its default.
		SilenceSeconds *float64 `json:"silence_seconds"` // audio silence, 0.2-5
		StableSeconds  *float64 `json:"stable_seconds"`  // partial text unchanged, 0 = off, 0.3-5
		StableRepeats  *int     `json:"stable_repeats"`  // identical partials in a row, 0 = off, 0-1000
	}
	if _, data, err := conn.ReadMessage(); err != nil || json.Unmarshal(data, &start) != nil || start.Type != "start" {
		send(map[string]any{"type": "error", "text": "expected a start message"})
		return
	}
	if start.Wake && start.WakeBackend != "vosk" && start.WakeBackend != "oww" {
		send(map[string]any{"type": "error", "text": "wake_backend must be vosk or oww"})
		return
	}
	if start.SampleRate < 8000 || start.SampleRate > 48000 {
		send(map[string]any{"type": "error", "text": "sample_rate must be between 8000 and 48000"})
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	dbg := newSTTDebug() // ~/.lucode/stt-bridge.log, see sttdebug.go
	defer dbg.Close()
	dbg.Printf("start: sample_rate=%d wake=%v backend=%q silence=%s stable=%s repeats=%s",
		start.SampleRate, start.Wake, start.WakeBackend, ptr(start.SilenceSeconds), ptr(start.StableSeconds), ptr(start.StableRepeats))

	send(map[string]any{"type": "loading"})
	c, _, err := stt.Ensure(ctx)
	if err != nil {
		send(map[string]any{"type": "error", "text": err.Error()})
		return
	}
	defer c.Close()
	if err := stt.WaitReady(ctx, c, 3*time.Minute); err != nil {
		send(map[string]any{"type": "error", "text": err.Error()})
		return
	}
	// takeover: a session left over from a closed tab must not block this one
	sess, err := c.Listen(ctx, stt.ListenConfig{
		SampleRate: start.SampleRate, Takeover: true,
		Wake: start.Wake, WakeBackend: start.WakeBackend, WakePhrase: start.WakePhrase,
		SilenceSeconds: clampSilence(start.SilenceSeconds),
		StableSeconds:  stableSecondsOr(start.StableSeconds),
		StableRepeats:  clampRepeats(start.StableRepeats),
	})
	if err != nil {
		send(map[string]any{"type": "error", "text": err.Error()})
		return
	}
	defer sess.Close()

	// worker -> browser. The page gets partials that replace each other and one final per
	// phrase; stt.Phrase keeps a bad final pass of the worker from replacing what was heard.
	end := stt.NewEndpointer(sttEndpointIdle())
	go func() {
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				c, ok := end.Tick(now)
				if !ok {
					continue
				}
				// Text unchanged for the idle time: the phrase is over. Send what was heard now
				// and tell the worker to close its recording too.
				if c.Text != "" {
					log.Printf("[stt] no changes for %v: closing the phrase with %q", end.Idle, c.Text)
					dbg.Printf("bridge: sin cambios %v, cierra la frase y manda a la pagina final %q", end.Idle, c.Text)
					send(map[string]any{"type": "final", "text": c.Text})
				} else {
					log.Printf("[stt] no changes for %v and no speech text: closing as noise", end.Idle)
					send(map[string]any{"type": "noise"})
				}
				_ = sess.Flush()
			}
		}
	}()
	go func() {
		for {
			ev, err := sess.Recv()
			if err == nil {
				dbg.Printf("worker: type=%d text=%q unchanged=%v where=%q", ev.Type, ev.Text, ev.Unchanged, ev.Where)
			}
			if err != nil {
				if ctx.Err() == nil {
					send(map[string]any{"type": "error", "text": "voice worker: " + err.Error()})
				}
				cancel()
				_ = conn.Close()
				return
			}
			switch ev.Type {
			case stt.EvReady:
				send(map[string]any{"type": "ready"})
			case stt.EvSpeechStart:
				end.Speech(time.Now())
				send(map[string]any{"type": "speech"})
			case stt.EvWake:
				send(map[string]any{"type": "wake"})
			case stt.EvWakeTimeout:
				send(map[string]any{"type": "wake_timeout"})
			case stt.EvWakePhrase:
				send(map[string]any{"type": "wake_phrase", "ok": ev.OK, "text": ev.Text})
			case stt.EvRealtime:
				if ev.Unchanged {
					break // same partial as before (changes=false): nothing new to show
				}
				if end.Partial(ev.Text, time.Now()) {
					send(map[string]any{"type": "realtime", "text": ev.Text})
				}
			case stt.EvFinal:
				text, fromPartial, ok := end.Final(ev.Text)
				if !ok {
					break // already closed and sent by the endpointer
				}
				if fromPartial {
					log.Printf("[stt] final %q was much shorter than what was heard; using %q", ev.Text, text)
				}
				dbg.Printf("bridge: final del worker %q -> a la pagina %q", ev.Text, text)
				send(map[string]any{"type": "final", "text": text})
			case stt.EvError:
				send(map[string]any{"type": "error", "text": ev.Text})
			}
		}
	}()

	// browser -> worker
	send(map[string]any{"type": "ready"})
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		switch mt {
		case websocket.BinaryMessage:
			dbg.Audio(data)
			if err := sess.SendAudio(data); err != nil {
				log.Printf("[stt] send audio: %v", err)
				return
			}
		case websocket.TextMessage:
			var m struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &m) == nil && m.Type == "flush" {
				dbg.Printf("pagina: flush")
				_ = sess.Flush()
			}
		}
	}
}
