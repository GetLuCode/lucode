package api

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// The raw capture (LUCODE_STT_TRACE_AUDIO=1) must be readable back: header, audio and flush records.
func TestSTTRawCaptureLayout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home) // os.UserHomeDir on Windows
	t.Setenv("HOME", home)
	t.Setenv("LUCODE_STT_TRACE_AUDIO", "1")

	d := newSTTDebug()
	d.StartRaw(16000)
	d.Audio([]byte{1, 2, 3, 4})
	d.Raw(rawFlush, nil)
	d.Close()

	b, err := os.ReadFile(filepath.Join(home, ".lucode", "stt-audio-last.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("LSTA1\n")) || binary.LittleEndian.Uint32(b[6:]) != 16000 {
		t.Fatalf("bad header: %q", b[:10])
	}
	b = b[10:]
	if b[0] != rawAudio || binary.LittleEndian.Uint32(b[5:]) != 4 || !bytes.Equal(b[9:13], []byte{1, 2, 3, 4}) {
		t.Fatalf("bad audio record: %v", b)
	}
	b = b[13:]
	if b[0] != rawFlush || binary.LittleEndian.Uint32(b[5:]) != 0 || len(b) != 9 {
		t.Fatalf("bad flush record: %v", b)
	}
}

// Without the variable nothing about the microphone is written.
func TestSTTRawCaptureOffByDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	t.Setenv("LUCODE_STT_TRACE_AUDIO", "")

	d := newSTTDebug()
	d.StartRaw(16000)
	d.Audio([]byte{1, 2})
	d.Close()
	if _, err := os.Stat(filepath.Join(home, ".lucode", "stt-audio-last.bin")); err == nil {
		t.Fatal("the raw capture must be off unless LUCODE_STT_TRACE_AUDIO=1")
	}
}
