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
//	client -> {"type":"start","sample_rate":48000,"wake":true,"wake_backend":"vosk","wake_phrase":"hey veronica"}   text, first message (wake* optional)
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
		StableSeconds: sttStableSeconds(),
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
			if err := sess.SendAudio(data); err != nil {
				log.Printf("[stt] send audio: %v", err)
				return
			}
		case websocket.TextMessage:
			var m struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(data, &m) == nil && m.Type == "flush" {
				_ = sess.Flush()
			}
		}
	}
}
