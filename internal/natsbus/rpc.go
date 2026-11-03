package natsbus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/nats-io/nats.go"
)

const rpcTimeout = 30 * time.Second

type rpcKey struct{}

// IsRPC reports whether a request was delivered by StartRPCServer, that is, sent by
// another lucode instance over NATS, as opposed to arriving on the HTTP port.
func IsRPC(ctx context.Context) bool {
	v, _ := ctx.Value(rpcKey{}).(bool)
	return v
}

type rpcEnvelope struct {
	Method   string              `json:"method"`
	Path     string              `json:"path"`
	RawQuery string              `json:"query,omitempty"`
	Headers  map[string][]string `json:"headers,omitempty"`
	Body     []byte              `json:"body,omitempty"`
}

type rpcReply struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    []byte              `json:"body,omitempty"`
	Err     string              `json:"error,omitempty"`
}

func rpcSubject(instanceID string) string {
	return fmt.Sprintf("lucode.rpc.%s", instanceID)
}

// StartRPCServer subscribes this instance to handle incoming HTTP-over-NATS requests.
// The handler should be the same http.Handler used by the HTTP server.
func (b *Bus) StartRPCServer(handler http.Handler) error {
	b.peersMu.Lock()
	id := b.selfInfo.ID
	b.peersMu.Unlock()

	if id == "" {
		return fmt.Errorf("discovery not started — call StartDiscovery first")
	}

	subj := rpcSubject(id)
	_, err := b.nc.Subscribe(subj, func(msg *nats.Msg) {
		go b.serveRPC(msg, handler)
	})
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", subj, err)
	}
	log.Printf("[rpc] serving on %s", subj)
	return nil
}

func (b *Bus) serveRPC(msg *nats.Msg, handler http.Handler) {
	var env rpcEnvelope
	if err := json.Unmarshal(msg.Data, &env); err != nil {
		replyErr(b.nc, msg.Reply, "bad envelope: "+err.Error())
		return
	}

	target := "http://localhost" + env.Path
	if env.RawQuery != "" {
		target += "?" + env.RawQuery
	}

	req, err := http.NewRequest(env.Method, target, bytes.NewReader(env.Body))
	if err != nil {
		replyErr(b.nc, msg.Reply, "build request: "+err.Error())
		return
	}
	for k, vs := range env.Headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	// Prevent infinite proxy loops
	req.Header.Del("X-Lucode-Remote")

	rw := &captureWriter{header: make(http.Header), code: 200}
	// Mark the request as one that came over NATS. The hub already authenticated the
	// user before forwarding it, and reaching this subject takes NATS access, so the
	// runner must not ask for a token of its own auth service. A context value (not a
	// header) so that nothing a client sends can fake it.
	handler.ServeHTTP(rw, req.WithContext(context.WithValue(req.Context(), rpcKey{}, true)))

	reply := rpcReply{
		Status:  rw.code,
		Headers: map[string][]string(rw.header),
		Body:    rw.buf.Bytes(),
	}
	data, _ := json.Marshal(reply)
	if msg.Reply != "" {
		b.nc.Publish(msg.Reply, data)
	}
}

// RPCRequest forwards an HTTP request to a remote lucode instance via NATS.
func (b *Bus) RPCRequest(instanceID string, r *http.Request) (*http.Response, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	env := rpcEnvelope{
		Method:   r.Method,
		Path:     r.URL.Path,
		RawQuery: r.URL.RawQuery,
		Headers:  map[string][]string(r.Header.Clone()),
		Body:     body,
	}
	data, _ := json.Marshal(env)

	msg, err := b.nc.Request(rpcSubject(instanceID), data, rpcTimeout)
	if err != nil {
		return nil, fmt.Errorf("nats request: %w", err)
	}

	var reply rpcReply
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		return nil, fmt.Errorf("parse reply: %w", err)
	}
	if reply.Err != "" {
		return nil, fmt.Errorf("remote error: %s", reply.Err)
	}

	hdr := make(http.Header)
	for k, vs := range reply.Headers {
		hdr[k] = vs
	}
	return &http.Response{
		StatusCode: reply.Status,
		Header:     hdr,
		Body:       io.NopCloser(bytes.NewReader(reply.Body)),
	}, nil
}

// captureWriter records the response written by an http.Handler.
type captureWriter struct {
	header http.Header
	code   int
	buf    bytes.Buffer
}

func (c *captureWriter) Header() http.Header         { return c.header }
func (c *captureWriter) WriteHeader(code int)         { c.code = code }
func (c *captureWriter) Write(b []byte) (int, error)  { return c.buf.Write(b) }

func replyErr(nc interface{ Publish(string, []byte) error }, replySubj, msg string) {
	if replySubj == "" {
		return
	}
	data, _ := json.Marshal(rpcReply{Status: 502, Err: msg})
	nc.Publish(replySubj, data)
}

// WithRPC marks ctx as that of a request delivered over NATS (used by tests of
// code that checks IsRPC).
func WithRPC(ctx context.Context) context.Context { return context.WithValue(ctx, rpcKey{}, true) }
