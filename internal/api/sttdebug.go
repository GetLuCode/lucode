package api

import (
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// sttDebug writes a small trace of one /ws/stt session to ~/.lucode/stt-bridge.log (overwritten
// at every session): what the page sends (audio per second: chunks and RMS, so a voice gate that
// cuts speech shows up), what the worker answers and what the page gets. It is for finding out
// why a phrase is misheard; LUCODE_STT_TRACE=0 turns it off.
type sttDebug struct {
	mu      sync.Mutex
	f       *os.File
	t0      time.Time
	winFrom time.Time
	chunks  int
	bytes   int
	sumSq   float64
	samples int
	maxRMS  float64
	lastAt  time.Time

	// LUCODE_STT_TRACE_AUDIO=1: the raw audio of the session too, with the time each chunk arrived, so a
	// phrase that went wrong can be replayed exactly (see raw below). It is the user's microphone on disk:
	// off unless asked for, and overwritten by the next session.
	raw *os.File
}

// Layout of ~/.lucode/stt-audio-last.bin: the magic "LSTA1" and a newline, the sample rate (uint32 LE) and then
// records [kind u8][ms since the start u32 LE][len u32 LE][len bytes]:
// kind 0 = PCM16 mono audio, kind 1 = flush (the page's or the bridge's, as it was sent to the worker).
const (
	rawAudio = 0
	rawFlush = 1
)

func newSTTDebug() *sttDebug {
	if os.Getenv("LUCODE_STT_TRACE") == "0" {
		return &sttDebug{}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return &sttDebug{}
	}
	dir := filepath.Join(home, ".lucode")
	_ = os.MkdirAll(dir, 0o755)
	f, err := os.Create(filepath.Join(dir, "stt-bridge.log"))
	if err != nil {
		return &sttDebug{}
	}
	d := &sttDebug{f: f, t0: time.Now()}
	if os.Getenv("LUCODE_STT_TRACE_AUDIO") == "1" {
		if raw, err := os.Create(filepath.Join(dir, "stt-audio-last.bin")); err == nil {
			d.raw = raw
		}
	}
	return d
}

// StartRaw writes the header of the raw capture (no-op when it is off).
func (d *sttDebug) StartRaw(sampleRate int) {
	if d.raw == nil {
		return
	}
	hdr := append([]byte("LSTA1\n"), 0, 0, 0, 0)
	binary.LittleEndian.PutUint32(hdr[6:], uint32(sampleRate))
	d.mu.Lock()
	defer d.mu.Unlock()
	_, _ = d.raw.Write(hdr)
}

// Raw appends one record of the raw capture (no-op when it is off).
func (d *sttDebug) Raw(kind byte, data []byte) {
	if d.raw == nil {
		return
	}
	rec := make([]byte, 9, 9+len(data))
	rec[0] = kind
	binary.LittleEndian.PutUint32(rec[1:], uint32(time.Since(d.t0).Milliseconds()))
	binary.LittleEndian.PutUint32(rec[5:], uint32(len(data)))
	rec = append(rec, data...)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.raw != nil {
		_, _ = d.raw.Write(rec)
	}
}

// ptr formats an optional value of the start message ("-" = not sent, the default applies).
func ptr[T any](p *T) string {
	if p == nil {
		return "-"
	}
	return fmt.Sprint(*p)
}

func (d *sttDebug) Close() {
	if d.f == nil {
		return
	}
	d.flushAudio(time.Now())
	d.mu.Lock()
	_ = d.f.Close()
	d.f = nil
	if d.raw != nil {
		_ = d.raw.Close()
		d.raw = nil
	}
	d.mu.Unlock()
}

func (d *sttDebug) Printf(format string, args ...any) {
	if d.f == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.f == nil {
		return
	}
	fmt.Fprintf(d.f, "%7.2fs  %s\n", time.Since(d.t0).Seconds(), fmt.Sprintf(format, args...))
}

// Audio accounts one binary message of the page (PCM16 mono) and prints a line per second.
func (d *sttDebug) Audio(pcm []byte) {
	if d.f == nil {
		return
	}
	d.Raw(rawAudio, pcm)
	now := time.Now()
	d.mu.Lock()
	if d.winFrom.IsZero() {
		d.winFrom = now
	}
	if gap := now.Sub(d.lastAt); !d.lastAt.IsZero() && gap > 400*time.Millisecond {
		d.mu.Unlock()
		d.Printf("audio: sin audio durante %.1f s (puerta de voz cerrada o micro mudo)", gap.Seconds())
		d.mu.Lock()
	}
	d.lastAt = now
	d.chunks++
	d.bytes += len(pcm)
	var sq float64
	n := len(pcm) / 2
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768
		sq += v * v
	}
	d.sumSq += sq
	d.samples += n
	if n > 0 {
		d.maxRMS = math.Max(d.maxRMS, math.Sqrt(sq/float64(n)))
	}
	due := now.Sub(d.winFrom) >= time.Second
	d.mu.Unlock()
	if due {
		d.flushAudio(now)
	}
}

func (d *sttDebug) flushAudio(now time.Time) {
	d.mu.Lock()
	chunks, bytes, samples, sumSq, maxRMS := d.chunks, d.bytes, d.samples, d.sumSq, d.maxRMS
	d.chunks, d.bytes, d.samples, d.sumSq, d.maxRMS = 0, 0, 0, 0, 0
	d.winFrom = time.Time{}
	d.mu.Unlock()
	if chunks == 0 {
		return
	}
	avg := 0.0
	if samples > 0 {
		avg = math.Sqrt(sumSq / float64(samples))
	}
	d.Printf("audio: %d trozos, %d bytes, RMS medio %.4f, maximo %.4f", chunks, bytes, avg, maxRMS)
}
