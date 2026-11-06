// Package fakeanthropic is a stand-in for the Anthropic Messages API, so tests exercise the
// real SDK over real HTTP without a key and without spending tokens.
//
// It refuses what the real API refuses for the models lucode uses, so a badly built request
// fails a test here instead of failing in production. The rules come from the API's docs;
// they are a subset, and passing here is not proof the real API accepts a request.
package fakeanthropic

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Reply scripts one response. Without one queued, the server answers a valid request by
// choosing the first value of the "outcome" enum in its output schema.
type Reply struct {
	Status     int    // non-200: an API error
	ErrType    string // for example rate_limit_error
	Message    string
	RetryAfter int

	Text            string // the body of the answer
	StopReason      string // default end_turn
	RefusalCategory string
}

// Request is what the server received.
type Request struct {
	Headers http.Header
	Body    map[string]any
}

// Server is a running fake.
type Server struct {
	URL string

	mu       sync.Mutex
	replies  []Reply
	requests []Request
}

// Start runs a fake until the test ends.
func Start(t testing.TB) *Server {
	t.Helper()
	s := &Server{}
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	s.URL = ts.URL
	return s
}

// Queue scripts the next responses, in order.
func (s *Server) Queue(r ...Reply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.replies = append(s.replies, r...)
}

// Requests returns what was received so far, including the requests it refused.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request{}, s.requests...)
}

var models = map[string]bool{"claude-opus-5-5": true, "claude-sonnet-5-5": true, "claude-haiku-5-5": true}

func apiError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("request-id", "req_fake")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"type": "error", "error": map[string]any{"type": typ, "message": msg}, "request_id": "req_fake",
	})
}

// validate applies the API's rules. It returns the status, type and message of the refusal.
func validate(h http.Header, b map[string]any) (int, string, string) {
	// the SDK sends an API key as a bearer token; x-api-key is the other accepted form
	if h.Get("x-api-key") == "" && !strings.HasPrefix(h.Get("Authorization"), "Bearer ") {
		return 401, "authentication_error", "an API key is required"
	}
	if h.Get("anthropic-version") == "" {
		return 400, "invalid_request_error", "anthropic-version header is required"
	}
	model, _ := b["model"].(string)
	if model == "" {
		return 400, "invalid_request_error", "model: Field required"
	}
	if !models[model] {
		return 404, "not_found_error", "model: " + model
	}
	if _, ok := b["max_tokens"].(float64); !ok {
		return 400, "invalid_request_error", "max_tokens: Field required"
	}
	msgs, _ := b["messages"].([]any)
	if len(msgs) == 0 {
		return 400, "invalid_request_error", "messages: at least one message is required"
	}
	if last, _ := msgs[len(msgs)-1].(map[string]any); last["role"] == "assistant" {
		return 400, "invalid_request_error", "this model does not support assistant message prefill"
	}
	for _, f := range []string{"temperature", "top_p", "top_k"} {
		if _, ok := b[f]; ok {
			return 400, "invalid_request_error", f + " is not supported on this model"
		}
	}
	if th, ok := b["thinking"].(map[string]any); ok && th["type"] != "adaptive" {
		return 400, "invalid_request_error", fmt.Sprintf("thinking.type %v is not supported on this model", th["type"])
	}
	if tc, ok := b["tool_choice"].(map[string]any); ok && (tc["type"] == "any" || tc["type"] == "tool") {
		return 400, "invalid_request_error", `tool_choice: type "tool" and "any" are not supported for this model.`
	}
	if _, ok := b["output_format"]; ok {
		return 400, "invalid_request_error", "output_format is deprecated: use output_config.format"
	}
	cfg, _ := b["output_config"].(map[string]any)
	if format, ok := cfg["format"].(map[string]any); ok {
		if format["type"] != "json_schema" {
			return 400, "invalid_request_error", "output_config.format.type must be json_schema"
		}
		schema, _ := format["schema"].(map[string]any)
		if schema["additionalProperties"] != false {
			return 400, "invalid_request_error", "output_config.format.schema: additionalProperties must be false"
		}
		if _, ok := schema["required"].([]any); !ok {
			return 400, "invalid_request_error", "output_config.format.schema: required is missing"
		}
	}
	betas := h.Get("anthropic-beta")
	switch f := b["fallbacks"].(type) {
	case nil:
	case string:
		if f != "default" || !strings.Contains(betas, "server-side-fallback-2026-07-01") {
			return 400, "invalid_request_error", `fallbacks: "default" requires the server-side-fallback-2026-07-01 beta`
		}
		if model == "claude-haiku-5-5" {
			return 400, "invalid_request_error", "fallbacks is not supported on this model"
		}
	default:
		if !strings.Contains(betas, "server-side-fallback-2026-06-01") {
			return 400, "invalid_request_error", "fallbacks: the array form requires the server-side-fallback-2026-06-01 beta"
		}
	}
	return 0, "", ""
}

// firstOutcome is the default answer: the first value the schema allows.
func firstOutcome(b map[string]any) string {
	cfg, _ := b["output_config"].(map[string]any)
	format, _ := cfg["format"].(map[string]any)
	schema, _ := format["schema"].(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	outcome, _ := props["outcome"].(map[string]any)
	values, _ := outcome["enum"].([]any)
	if len(values) == 0 {
		return `{}`
	}
	out, _ := json.Marshal(map[string]any{"outcome": values[0], "reason": "fake answer"})
	return string(out)
}

// ServeHTTP answers POST /v1/messages.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
		apiError(w, 404, "not_found_error", r.Method+" "+r.URL.Path)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		apiError(w, 400, "invalid_request_error", "the body is not valid JSON")
		return
	}
	s.mu.Lock()
	s.requests = append(s.requests, Request{Headers: r.Header.Clone(), Body: body})
	var reply Reply
	scripted := false
	s.mu.Unlock()

	if status, typ, msg := validate(r.Header, body); status != 0 {
		apiError(w, status, typ, msg)
		return
	}
	s.mu.Lock()
	if len(s.replies) > 0 {
		reply, s.replies, scripted = s.replies[0], s.replies[1:], true
	}
	s.mu.Unlock()

	if reply.Status != 0 && reply.Status != 200 {
		if reply.RetryAfter > 0 {
			w.Header().Set("retry-after", strconv.Itoa(reply.RetryAfter))
		}
		apiError(w, reply.Status, reply.ErrType, reply.Message)
		return
	}
	text := reply.Text
	if !scripted {
		text = firstOutcome(body)
	}
	stop := reply.StopReason
	if stop == "" {
		stop = "end_turn"
	}
	// thinking is always on for these models, and its text is empty by default
	content := []map[string]any{{"type": "thinking", "thinking": "", "signature": "fake-signature"}}
	var details any
	if stop == "refusal" {
		details = map[string]any{"type": "refusal", "category": reply.RefusalCategory, "explanation": "declined by the fake"}
	} else if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"id": "msg_fake", "type": "message", "role": "assistant", "model": body["model"],
		"content": content, "stop_reason": stop, "stop_sequence": nil, "stop_details": details,
		"usage": map[string]any{
			"input_tokens": 123, "output_tokens": 45, "cache_creation_input_tokens": 0, "cache_read_input_tokens": 0,
		},
	})
}
