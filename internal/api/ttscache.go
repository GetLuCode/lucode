package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Disk cache of the synthesized audio (~/.lucode/tts-cache/<hash>.pcm, PCM16 mono 44100 Hz), so playing the
// same text again does not pay Fish Audio again. The key is the text plus what changes the sound (model, voice,
// speed); never the API key. An entry is written to a temporary file while it streams and renamed only when the
// synthesis finished, so an aborted playback never leaves a truncated audio behind.

const ttsCacheDefaultMB = 500

var ttsCacheMu sync.Mutex // serializes pruning

func ttsCacheDir() string { return filepath.Join(filepath.Dir(ttsPath()), "tts-cache") }

func ttsCacheKey(s ttsSettings, text string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%g\x00%s", s.Model, s.VoiceID, s.Speed, text)))
	return hex.EncodeToString(h[:])
}

func ttsCacheLimit() int64 {
	mb := ttsCacheDefaultMB
	if v, err := strconv.Atoi(os.Getenv("LUCODE_TTS_CACHE_MB")); err == nil {
		mb = v
	}
	return int64(mb) << 20 // 0 or less = cache off
}

// ttsCacheOpen returns the cached audio for key, or nil. Opening it counts as a use (for the pruning order).
func ttsCacheOpen(key string) *os.File {
	if ttsCacheLimit() <= 0 {
		return nil
	}
	p := filepath.Join(ttsCacheDir(), key+".pcm")
	f, err := os.Open(p)
	if err != nil {
		return nil
	}
	if st, err := f.Stat(); err != nil || st.Size() == 0 {
		f.Close()
		return nil
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now)
	return f
}

// ttsCacheWriter collects one synthesis. Write copies the audio; Commit keeps it, Discard drops it.
type ttsCacheWriter struct {
	f    *os.File
	key  string
	bad  bool
	size int64
}

func ttsCacheCreate(key string) *ttsCacheWriter {
	if ttsCacheLimit() <= 0 {
		return nil
	}
	dir := ttsCacheDir()
	if os.MkdirAll(dir, 0o755) != nil {
		return nil
	}
	f, err := os.CreateTemp(dir, "tmp-*.part")
	if err != nil {
		return nil
	}
	return &ttsCacheWriter{f: f, key: key}
}

func (w *ttsCacheWriter) Write(p []byte) {
	if w == nil || w.bad {
		return
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	if err != nil {
		w.bad = true
	}
}

func (w *ttsCacheWriter) Discard() {
	if w == nil {
		return
	}
	name := w.f.Name()
	w.f.Close()
	_ = os.Remove(name)
}

func (w *ttsCacheWriter) Commit() {
	if w == nil {
		return
	}
	if w.bad || w.size == 0 {
		w.Discard()
		return
	}
	name := w.f.Name()
	if w.f.Close() != nil {
		_ = os.Remove(name)
		return
	}
	if os.Rename(name, filepath.Join(ttsCacheDir(), w.key+".pcm")) != nil {
		_ = os.Remove(name)
		return
	}
	ttsCachePrune()
}

// ttsCachePrune deletes the least recently used entries until the cache fits its limit, and any leftover
// temporary file older than an hour (a crash in the middle of a synthesis).
func ttsCachePrune() {
	ttsCacheMu.Lock()
	defer ttsCacheMu.Unlock()
	entries, err := os.ReadDir(ttsCacheDir())
	if err != nil {
		return
	}
	type item struct {
		path string
		size int64
		mod  time.Time
	}
	var items []item
	var total int64
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		p := filepath.Join(ttsCacheDir(), e.Name())
		if filepath.Ext(e.Name()) == ".part" {
			if time.Since(info.ModTime()) > time.Hour {
				_ = os.Remove(p)
			}
			continue
		}
		items = append(items, item{p, info.Size(), info.ModTime()})
		total += info.Size()
	}
	limit := ttsCacheLimit()
	if total <= limit {
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	for _, it := range items {
		if total <= limit {
			break
		}
		if os.Remove(it.path) == nil {
			total -= it.size
		}
	}
}
