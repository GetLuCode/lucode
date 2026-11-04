package stt

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
)

// ListenEvent (voice.proto): type=1, text=2, where=3, ok=4, changes=5 (optional bool).
func realtimeEvent(text string, changes *bool) []byte {
	b := protowire.AppendTag(nil, 1, protowire.VarintType)
	b = protowire.AppendVarint(b, EvRealtime)
	if text != "" {
		b = protowire.AppendTag(b, 2, protowire.BytesType)
		b = protowire.AppendString(b, text)
	}
	if changes != nil {
		b = protowire.AppendTag(b, 5, protowire.VarintType)
		if *changes {
			b = protowire.AppendVarint(b, 1)
		} else {
			b = protowire.AppendVarint(b, 0)
		}
	}
	return b
}

func TestDecodeEventChanges(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name      string
		raw       []byte
		text      string
		unchanged bool
	}{
		{"changed partial carries its text", realtimeEvent("hola", &yes), "hola", false},
		{"changes=false has no text and is unchanged", realtimeEvent("", &no), "", true},
		{"worker without the field: the event carries text", realtimeEvent("hola", nil), "hola", false},
	}
	for _, c := range cases {
		ev := decodeEvent(c.raw)
		if ev.Type != EvRealtime || ev.Text != c.text || ev.Unchanged != c.unchanged {
			t.Errorf("%s: got type=%d text=%q unchanged=%v", c.name, ev.Type, ev.Text, ev.Unchanged)
		}
	}
}
