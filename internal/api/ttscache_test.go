package api

import (
	"os"
	"testing"
	"time"
)

func useTempHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
}

func TestTTSCacheRoundTrip(t *testing.T) {
	useTempHome(t)
	s := ttsSettings{Model: "s1", VoiceID: "v", Speed: 1}
	key := ttsCacheKey(s, "hola")
	if ttsCacheOpen(key) != nil {
		t.Fatal("empty cache must miss")
	}
	w := ttsCacheCreate(key)
	w.Write([]byte{1, 2, 3, 4})
	w.Commit()
	f := ttsCacheOpen(key)
	if f == nil {
		t.Fatal("committed entry must hit")
	}
	f.Close()

	// text, voice or speed change the key; the API key is not part of it
	if ttsCacheKey(s, "adios") == key || ttsCacheKey(ttsSettings{Model: "s1", VoiceID: "v", Speed: 1.5}, "hola") == key {
		t.Fatal("key must depend on text and speed")
	}
	if ttsCacheKey(ttsSettings{APIKey: "secret", Model: "s1", VoiceID: "v", Speed: 1}, "hola") != key {
		t.Fatal("key must not depend on the API key")
	}
}

func TestTTSCacheDiscardKeepsNothing(t *testing.T) {
	useTempHome(t)
	key := ttsCacheKey(ttsSettings{}, "x")
	w := ttsCacheCreate(key)
	w.Write([]byte{1, 2})
	w.Discard()
	if ttsCacheOpen(key) != nil {
		t.Fatal("a discarded synthesis must not be cached")
	}
	if es, _ := os.ReadDir(ttsCacheDir()); len(es) != 0 {
		t.Fatalf("temporary file left behind: %v", es)
	}
}

func TestTTSCachePrunesOldest(t *testing.T) {
	useTempHome(t)
	t.Setenv("LUCODE_TTS_CACHE_MB", "1")
	chunk := make([]byte, 600<<10) // 600 KB each: two do not fit in 1 MB
	for i, k := range []string{"a", "b"} {
		w := ttsCacheCreate(k)
		w.Write(chunk)
		w.Commit()
		old := time.Now().Add(time.Duration(i-5) * time.Hour)
		_ = os.Chtimes(ttsCacheDir()+"/"+k+".pcm", old, old)
		if i == 0 {
			continue
		}
	}
	// "a" is the older one and the second commit pruned it
	if ttsCacheOpen("a") != nil {
		t.Fatal("oldest entry must be pruned")
	}
	if f := ttsCacheOpen("b"); f == nil {
		t.Fatal("newest entry must stay")
	} else {
		f.Close()
	}
}

func TestTTSCacheOff(t *testing.T) {
	useTempHome(t)
	t.Setenv("LUCODE_TTS_CACHE_MB", "0")
	if w := ttsCacheCreate("k"); w != nil {
		t.Fatal("cache must be off with LUCODE_TTS_CACHE_MB=0")
	}
}
