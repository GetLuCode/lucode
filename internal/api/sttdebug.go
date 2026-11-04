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
}

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
	return &sttDebug{f: f, t0: time.Now()}
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
