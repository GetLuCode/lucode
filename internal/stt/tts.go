package stt

import (
	"context"
	"errors"
	"io"
	"math"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protowire"
)

// TTSRequest is the subset of SynthesizeRequest lucode sets (voice.proto). Audio comes
// back as PCM16 mono little-endian at 44100 Hz (the worker's default).
type TTSRequest struct {
	Text    string
	APIKey  string
	Model   string  // "" = worker default (s2.1-pro-free)
	VoiceID string  // "" = the model's default voice
	Speed   float64 // 0 = default
}

func encodeTTS(r TTSRequest) []byte {
	var b []byte
	str := func(num protowire.Number, s string) {
		if s != "" {
			b = protowire.AppendTag(b, num, protowire.BytesType)
			b = protowire.AppendString(b, s)
		}
	}
	str(1, r.Text)
	str(2, r.APIKey)
	str(3, r.Model)
	str(4, r.VoiceID)
	if r.Speed > 0 {
		b = protowire.AppendTag(b, 5, protowire.Fixed64Type)
		b = protowire.AppendFixed64(b, math.Float64bits(r.Speed))
	}
	return b
}

// SampleRate is the rate of the PCM16 audio Synthesize returns by default.
const SampleRate = 44100

// Synthesize streams the audio of r to onAudio as the worker produces it. gRPC errors
// (PERMISSION_DENIED for a bad key or no credit, FAILED_PRECONDITION, ...) are returned as is.
func (c *Client) Synthesize(ctx context.Context, r TTSRequest, onAudio func(pcm []byte) error) error {
	stream, err := c.conn.NewStream(c.ctx(ctx), &grpc.StreamDesc{ServerStreams: true},
		"/voice.v1.VoiceEngine/Synthesize", grpc.ForceCodec(rawCodec{}))
	if err != nil {
		return err
	}
	if err := stream.SendMsg(encodeTTS(r)); err != nil {
		return err
	}
	if err := stream.CloseSend(); err != nil {
		return err
	}
	for {
		var msg []byte
		if err := stream.RecvMsg(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		// SynthesizeReply: oneof header = 1, audio = 2, summary = 3
		for b := msg; len(b) > 0; {
			num, typ, n := protowire.ConsumeTag(b)
			if n < 0 {
				break
			}
			b = b[n:]
			if num == 2 && typ == protowire.BytesType {
				v, m := protowire.ConsumeBytes(b)
				b = b[m:]
				if err := onAudio(v); err != nil {
					return err
				}
				continue
			}
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				break
			}
			b = b[m:]
		}
	}
}
