package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// Drives /ws/stt like the browser does (two phrases with silence between them) and
// prints what the page would receive. Skipped unless LUCODE_STT_LIVE=1.
func TestLiveSTTSocket(t *testing.T) {
	if os.Getenv("LUCODE_STT_LIVE") != "1" {
		t.Skip("set LUCODE_STT_LIVE=1 to run against the real voice worker")
	}
	h := &Handler{}
	srv := httptest.NewServer(httptestHandler(h))
	defer srv.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/stt", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.WriteJSON(map[string]any{"type": "start", "sample_rate": 22050})

	t0 := time.Now()
	ready := make(chan struct{}, 4)
	go func() {
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			t.Logf("%5.1fs %v %v", time.Since(t0).Seconds(), m["type"], m["text"])
			if m["type"] == "ready" {
				ready <- struct{}{}
			}
		}
	}()
	<-ready // like the page: audio starts flowing as soon as the bridge says ready

	dir := filepath.Join(`C:\projects\fishaudio_oct\SttApp`, "models", "sherpa-onnx-nemo-parakeet-tdt-0.6b-v3-int8", "test_wavs")
	silence := make([]byte, 22050*2*2)
	for _, name := range []string{"es.wav", "de.wav"} {
		wav, _ := os.ReadFile(filepath.Join(dir, name))
		for _, pcm := range [][]byte{wav[44:], silence} {
			chunk := 22050 / 25 * 2
			for off := 0; off < len(pcm); off += chunk {
				end := off + chunk
				if end > len(pcm) {
					end = len(pcm)
				}
				time.Sleep(40 * time.Millisecond)
				_ = conn.WriteMessage(websocket.BinaryMessage, pcm[off:end])
			}
		}
	}
	time.Sleep(5 * time.Second)
}
