// Package stt talks to the voice worker (stt_worker.py of SttApp): a Python gRPC
// server that runs Parakeet speech recognition. The browser cannot speak gRPC, so
// lucode relays: browser WebSocket <-> this client <-> worker.
//
// The protobuf messages are tiny, so they are encoded by hand (protowire) instead of
// generating code: no protoc needed. Field numbers follow voice.proto.
package stt

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
)

// rawCodec passes already-encoded protobuf bytes through ("proto" is the content
// subtype the Python server expects).
type rawCodec struct{}

func (rawCodec) Name() string { return "proto" }
func (rawCodec) Marshal(v any) ([]byte, error) {
	b, ok := v.([]byte)
	if !ok {
		return nil, fmt.Errorf("stt: cannot marshal %T", v)
	}
	return b, nil
}
func (rawCodec) Unmarshal(data []byte, v any) error {
	p, ok := v.(*[]byte)
	if !ok {
		return fmt.Errorf("stt: cannot unmarshal into %T", v)
	}
	*p = append((*p)[:0], data...)
	return nil
}

// Event types of ListenEvent (voice.proto).
const (
	EvReady       = 1
	EvSpeechStart = 2
	EvWake        = 3
	EvWakeTimeout = 4
	EvRealtime    = 5
	EvFinal       = 6
	EvGPU         = 7
	EvWakePhrase  = 8
	EvError       = 9
)

// Event is one ListenEvent.
type Event struct {
	Type  int
	Text  string
	Where string
	OK    bool
	// Unchanged is a REALTIME with changes=false (field 5): the partial is the same as the last
	// one the worker sent, so the event carries no text. It must not be shown or counted as text.
	Unchanged bool
}

// Info is the part of EngineInfo lucode uses.
type Info struct {
	EngineKey string
	Ready     bool
	Listening bool
	PID       int
}

// Client is a connection to one worker. No token is sent: the shared worker listens on
// 127.0.0.1 only and runs with --no-token (see launch.go).
type Client struct {
	conn *grpc.ClientConn
}

// Dial connects (lazily) to a worker on 127.0.0.1:port.
func Dial(port int) (*Client, error) {
	conn, err := grpc.NewClient(fmt.Sprintf("127.0.0.1:%d", port), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// NeedsToken reports that the worker answered but wants an x-stt-token: it was started
// with a token (for example by an older SttApp) instead of --no-token.
func NeedsToken(err error) bool { return status.Code(err) == codes.Unauthenticated }

// GetInfo asks the worker about itself; an error means it is not reachable.
func (c *Client) GetInfo(ctx context.Context) (Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var out []byte
	err := c.conn.Invoke(ctx, "/voice.v1.VoiceEngine/GetInfo", []byte{}, &out, grpc.ForceCodec(rawCodec{}))
	if err != nil {
		return Info{}, err
	}
	var info Info
	for b := out; len(b) > 0; {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		switch {
		case num == 2 && typ == protowire.BytesType:
			v, m := protowire.ConsumeString(b)
			info.EngineKey, b = v, b[m:]
		case typ == protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			b = b[m:]
			switch num {
			case 3:
				info.Ready = v != 0
			case 4:
				info.Listening = v != 0
			case 7:
				info.PID = int(v)
			}
		default:
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return info, nil
			}
			b = b[m:]
		}
	}
	return info, nil
}

// Shutdown asks the worker to exit.
func (c *Client) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var out []byte
	return c.conn.Invoke(ctx, "/voice.v1.VoiceEngine/Shutdown", []byte{}, &out, grpc.ForceCodec(rawCodec{}))
}

// ListenConfig is the subset of ListenConfig lucode sets.
type ListenConfig struct {
	SampleRate     int
	SilenceSeconds float64 // 0 = worker default
	StableSeconds  float64 // 0 = off
	StableRepeats  *int    // identical partials in a row that close a phrase; nil = worker default (10), 0 = off
	Takeover       bool
	// Hotword: only transcribe after the wake word. Backend is "vosk" (Phrase is the wake
	// phrase) or "oww" (openWakeWord, "hey jarvis").
	Wake        bool
	WakeBackend string // "vosk", "oww", or "external" (the client detects the hotword itself; see SetPark)
	WakePhrase  string
	// Park: while waiting for the hotword the worker frees the GPU model (Unload: dropped from VRAM,
	// not moved to CPU/RAM) after UnloadAfter seconds (nil = worker default, 30). It comes back when
	// the hotword is heard, or, with WakeBackend "external", when the client calls SetPark(false).
	Park        bool
	Unload      bool
	UnloadAfter *float64
}

func encodeConfig(cfg ListenConfig) []byte {
	var m []byte
	if cfg.Takeover {
		m = protowire.AppendTag(m, 1, protowire.VarintType)
		m = protowire.AppendVarint(m, 1)
	}
	if cfg.SampleRate > 0 {
		m = protowire.AppendTag(m, 2, protowire.VarintType)
		m = protowire.AppendVarint(m, uint64(cfg.SampleRate))
	}
	if cfg.SilenceSeconds > 0 {
		m = protowire.AppendTag(m, 3, protowire.Fixed64Type)
		m = protowire.AppendFixed64(m, math.Float64bits(cfg.SilenceSeconds))
	}
	m = protowire.AppendTag(m, 4, protowire.VarintType) // partials
	m = protowire.AppendVarint(m, 1)
	if cfg.Wake {
		m = protowire.AppendTag(m, 5, protowire.VarintType)
		m = protowire.AppendVarint(m, 1)
		if cfg.WakeBackend != "" {
			m = protowire.AppendTag(m, 6, protowire.BytesType)
			m = protowire.AppendString(m, cfg.WakeBackend)
		}
		if cfg.WakePhrase != "" {
			m = protowire.AppendTag(m, 7, protowire.BytesType)
			m = protowire.AppendString(m, cfg.WakePhrase)
		}
	}
	if cfg.Park {
		m = protowire.AppendTag(m, 8, protowire.VarintType)
		m = protowire.AppendVarint(m, 1)
	}
	if cfg.StableSeconds > 0 {
		m = protowire.AppendTag(m, 9, protowire.Fixed64Type)
		m = protowire.AppendFixed64(m, math.Float64bits(cfg.StableSeconds))
	}
	if cfg.Unload {
		m = protowire.AppendTag(m, 10, protowire.VarintType)
		m = protowire.AppendVarint(m, 1)
	}
	if cfg.UnloadAfter != nil {
		m = protowire.AppendTag(m, 11, protowire.Fixed64Type)
		m = protowire.AppendFixed64(m, math.Float64bits(*cfg.UnloadAfter))
	}
	if cfg.StableRepeats != nil {
		m = protowire.AppendTag(m, 12, protowire.VarintType)
		m = protowire.AppendVarint(m, uint64(*cfg.StableRepeats))
	}
	req := protowire.AppendTag(nil, 1, protowire.BytesType)
	return protowire.AppendBytes(req, m)
}

// encodePark is a ListenConfig with only `park` set: sent in the middle of a session it changes
// just that option (the worker keeps the rest).
func encodePark(park bool) []byte {
	var m []byte
	m = protowire.AppendTag(m, 8, protowire.VarintType)
	if park {
		m = protowire.AppendVarint(m, 1)
	} else {
		m = protowire.AppendVarint(m, 0)
	}
	req := protowire.AppendTag(nil, 1, protowire.BytesType)
	return protowire.AppendBytes(req, m)
}

func encodeAudio(pcm []byte) []byte {
	b := protowire.AppendTag(nil, 2, protowire.BytesType)
	return protowire.AppendBytes(b, pcm)
}

func encodeFlush() []byte {
	b := protowire.AppendTag(nil, 3, protowire.VarintType)
	return protowire.AppendVarint(b, 1)
}

func decodeEvent(b []byte) Event {
	var ev Event
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			break
		}
		b = b[n:]
		switch typ {
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			b = b[m:]
			switch num {
			case 1:
				ev.Type = int(v)
			case 4:
				ev.OK = v != 0
			case 5:
				ev.Unchanged = v == 0 // `changes`; absent (older workers) means the event carries text
			}
		case protowire.BytesType:
			v, m := protowire.ConsumeString(b)
			b = b[m:]
			switch num {
			case 2:
				ev.Text = v
			case 3:
				ev.Where = v
			}
		default:
			m := protowire.ConsumeFieldValue(num, typ, b)
			if m < 0 {
				return ev
			}
			b = b[m:]
		}
	}
	return ev
}

// Session is one Listen stream.
type Session struct {
	stream grpc.ClientStream
	cancel context.CancelFunc
	sendMu sync.Mutex // SendMsg must not be called concurrently
}

// Listen opens a recognition session with cfg.
func (c *Client) Listen(ctx context.Context, cfg ListenConfig) (*Session, error) {
	ctx, cancel := context.WithCancel(ctx)
	stream, err := c.conn.NewStream(ctx, &grpc.StreamDesc{ClientStreams: true, ServerStreams: true},
		"/voice.v1.VoiceEngine/Listen", grpc.ForceCodec(rawCodec{}))
	if err != nil {
		cancel()
		return nil, err
	}
	s := &Session{stream: stream, cancel: cancel}
	if err := stream.SendMsg(encodeConfig(cfg)); err != nil {
		cancel()
		return nil, err
	}
	return s, nil
}

// SendAudio sends PCM16 mono little-endian at the configured sample rate.
func (s *Session) SendAudio(pcm []byte) error { return s.send(encodeAudio(pcm)) }

func (s *Session) send(msg []byte) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.SendMsg(msg)
}

// SetPark turns the parking on or off during the session. With it off the worker brings the model
// back to the GPU right away (loading it from disk if it was unloaded); with it on it frees it
// again after the session's UnloadAfter. Only effective when the session was opened with Wake.
func (s *Session) SetPark(park bool) error { return s.send(encodePark(park)) }

// Flush closes the phrase in progress.
func (s *Session) Flush() error { return s.send(encodeFlush()) }

// Recv returns the next event; io.EOF when the worker ends the stream.
func (s *Session) Recv() (Event, error) {
	var b []byte
	if err := s.stream.RecvMsg(&b); err != nil {
		return Event{}, err
	}
	return decodeEvent(b), nil
}

// Close ends the session.
func (s *Session) Close() {
	_ = s.stream.CloseSend()
	s.cancel()
}
