package stt

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Live test against the real voice worker (starts it with the int8 model, ~0.6 GB of
// RAM). Skipped unless LUCODE_STT_LIVE=1. It streams a recorded Spanish phrase at real
// time pace, like the browser does, and checks that a transcript comes back.
func TestLiveListen(t *testing.T) {
	if os.Getenv("LUCODE_STT_LIVE") != "1" {
		t.Skip("set LUCODE_STT_LIVE=1 to run against the real voice worker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	c, _, err := Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	start := time.Now()
	if err := WaitReady(ctx, c, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	t.Logf("model ready after %v", time.Since(start))

	wav, err := os.ReadFile(filepath.Join(envOr("LUCODE_STT_DIR", defaultDir), "models", modelName, "test_wavs", "es.wav"))
	if err != nil {
		t.Fatal(err)
	}
	rate := int(binary.LittleEndian.Uint32(wav[24:28]))
	pcm := wav[44:]

	sess, err := c.Listen(ctx, ListenConfig{SampleRate: rate, Takeover: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	var finals []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			ev, err := sess.Recv()
			if err != nil {
				return
			}
			t.Logf("event %d %q", ev.Type, ev.Text)
			if ev.Type == EvFinal && strings.TrimSpace(ev.Text) != "" {
				finals = append(finals, ev.Text)
				return
			}
		}
	}()

	chunk := rate / 25 * 2 // 40 ms of 16-bit mono
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for off := 0; off < len(pcm); off += chunk {
		end := off + chunk
		if end > len(pcm) {
			end = len(pcm)
		}
		<-tick.C
		if err := sess.SendAudio(pcm[off:end]); err != nil {
			t.Fatal(err)
		}
	}
	_ = sess.Flush()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		t.Fatal("no final transcript")
	}
	if len(finals) == 0 {
		t.Fatal("stream ended without a final transcript")
	}
	t.Logf("final: %q", finals[0])
}

// A made-up key must be rejected by Fish Audio with PERMISSION_DENIED (no credit is spent).
func TestLiveSynthesizeBadKey(t *testing.T) {
	if os.Getenv("LUCODE_STT_LIVE") != "1" {
		t.Skip("set LUCODE_STT_LIVE=1 to run against the real voice worker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c, _, err := Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	err = c.Synthesize(ctx, TTSRequest{Text: "hola", APIKey: "not-a-real-key"}, func([]byte) error { return nil })
	if err == nil {
		t.Fatal("expected an error for a bad key")
	}
	t.Logf("error: %v", err)
	if code := status.Code(err); code != codes.PermissionDenied && code != codes.Unavailable && code != codes.Internal {
		t.Fatalf("unexpected code %v", code)
	}
}

// A wake phrase is validated by the worker (Vosk model); an old worker without that
// model is replaced by Ensure.
func TestLiveWakePhrase(t *testing.T) {
	if os.Getenv("LUCODE_STT_LIVE") != "1" {
		t.Skip("set LUCODE_STT_LIVE=1 to run against the real voice worker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	c, _, err := Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := WaitReady(ctx, c, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	sess, err := c.Listen(ctx, ListenConfig{SampleRate: 16000, Takeover: true, Wake: true, WakeBackend: "vosk", WakePhrase: "hey veronica"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	deadline := time.After(30 * time.Second)
	got := make(chan Event, 8)
	go func() {
		for {
			ev, err := sess.Recv()
			if err != nil {
				return
			}
			got <- ev
		}
	}()
	for {
		select {
		case ev := <-got:
			t.Logf("event %d ok=%v %q", ev.Type, ev.OK, ev.Text)
			if ev.Type == EvWakePhrase {
				if !ev.OK {
					t.Fatalf("wake phrase rejected: %s", ev.Text)
				}
				return
			}
		case <-deadline:
			t.Fatal("no WAKE_PHRASE event")
		}
	}
}

// Prints every event for two phrases back to back, to see how partials and finals relate.
func TestLiveTrace(t *testing.T) {
	if os.Getenv("LUCODE_STT_LIVE") != "1" {
		t.Skip("set LUCODE_STT_LIVE=1 to run against the real voice worker")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	c, _, err := Ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := WaitReady(ctx, c, 3*time.Minute); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(envOr("LUCODE_STT_DIR", defaultDir), "models", modelName, "test_wavs")
	sess, err := c.Listen(ctx, ListenConfig{SampleRate: 22050, Takeover: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	t0 := time.Now()
	go func() {
		for {
			ev, err := sess.Recv()
			if err != nil {
				return
			}
			t.Logf("%5.1fs type=%d %q", time.Since(t0).Seconds(), ev.Type, ev.Text)
		}
	}()
	silence := make([]byte, 22050*2*2) // 2 s
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
				_ = sess.SendAudio(pcm[off:end])
			}
		}
	}
	time.Sleep(4 * time.Second)
}
