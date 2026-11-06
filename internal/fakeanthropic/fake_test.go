package fakeanthropic

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// A fake that accepts anything proves nothing: it has to refuse what the real API refuses.
func TestItRefusesWhatTheAPIRefuses(t *testing.T) {
	good := func() map[string]any {
		return map[string]any{
			"model": "claude-opus-5-5", "max_tokens": 100,
			"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		}
	}
	with := func(k string, v any) map[string]any {
		b := good()
		b[k] = v
		return b
	}
	without := func(k string) map[string]any {
		b := good()
		delete(b, k)
		return b
	}
	schema := func(s map[string]any) map[string]any {
		return map[string]any{"format": map[string]any{"type": "json_schema", "schema": s}}
	}
	auth := map[string]string{"Authorization": "Bearer k", "anthropic-version": "2023-06-01"}
	for _, c := range []struct {
		name    string
		headers map[string]string
		body    map[string]any
		status  int
		want    string
	}{
		{"valid", auth, good(), 200, ""},
		{"x-api-key is also valid", map[string]string{"x-api-key": "k", "anthropic-version": "2023-06-01"}, good(), 200, ""},
		{"no key", map[string]string{"anthropic-version": "2023-06-01"}, good(), 401, "authentication_error"},
		{"no version", map[string]string{"Authorization": "Bearer k"}, good(), 400, "anthropic-version"},
		{"unknown model", auth, with("model", "claude-3-opus"), 404, "not_found_error"},
		{"no max_tokens", auth, without("max_tokens"), 400, "max_tokens"},
		{"temperature", auth, with("temperature", 0.5), 400, "temperature"},
		{"thinking disabled", auth, with("thinking", map[string]any{"type": "disabled"}), 400, "thinking"},
		{"thinking budget", auth, with("thinking", map[string]any{"type": "enabled", "budget_tokens": 2000}), 400, "thinking"},
		{"forced tool", auth, with("tool_choice", map[string]any{"type": "tool", "name": "x"}), 400, "tool_choice"},
		{"old output_format", auth, with("output_format", map[string]any{}), 400, "output_format"},
		{"open schema", auth, with("output_config", schema(map[string]any{"required": []any{"a"}})), 400, "additionalProperties"},
		{"schema without required", auth, with("output_config", schema(map[string]any{"additionalProperties": false})), 400, "required"},
		{"fallback without its beta", auth, with("fallbacks", "default"), 400, "server-side-fallback-2026-07-01"},
		{"prefill", auth, with("messages", []any{map[string]any{"role": "user", "content": "a"}, map[string]any{"role": "assistant", "content": "b"}}), 400, "prefill"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := Start(t)
			raw, _ := json.Marshal(c.body)
			req, _ := http.NewRequest("POST", s.URL+"/v1/messages", bytes.NewReader(raw))
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != c.status || !strings.Contains(string(body), c.want) {
				t.Fatalf("%d %s, want %d %q", res.StatusCode, body, c.status, c.want)
			}
			if c.status != 200 && !strings.Contains(string(body), `"type":"error"`) {
				t.Fatalf("not the API's error shape: %s", body)
			}
		})
	}
}
