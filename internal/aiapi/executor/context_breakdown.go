package executor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"log"
	"sync"
)

// staticTokens is the part of the prompt that is identical on every request of
// a session: the system blocks and the tool definitions.
type staticTokens struct {
	System int
	Tools  int
}

var staticTokenCache sync.Map // key (model + hash of system/tools) -> staticTokens

// staticContextTokens measures the system prompt and tool definitions once per
// (model, system, tools) combination. Claude models are measured exactly with
// count_tokens; anything else (or a failed call) falls back to ~4 chars/token.
func (c *Client) staticContextTokens(ctx context.Context, model string, system, tools []map[string]any) staticTokens {
	sb, _ := json.Marshal(system)
	tb, _ := json.Marshal(tools)
	h := sha256.New()
	h.Write([]byte(model))
	h.Write(sb)
	h.Write(tb)
	key := string(h.Sum(nil))
	if v, ok := staticTokenCache.Load(key); ok {
		return v.(staticTokens)
	}

	st := staticTokens{System: len(sb) / 4, Tools: len(tb) / 4}
	if id, ok := ResolveClaudeModel(model); ok {
		base, e1 := claudeCountTokens(ctx, c.HTTP, id, nil, nil)
		withSys, e2 := claudeCountTokens(ctx, c.HTTP, id, system, nil)
		withAll, e3 := claudeCountTokens(ctx, c.HTTP, id, system, tools)
		if e1 == nil && e2 == nil && e3 == nil {
			st = staticTokens{System: max(withSys-base, 0), Tools: max(withAll-withSys, 0)}
		} else {
			log.Printf("[context] count_tokens failed, estimating: %v %v %v", e1, e2, e3)
			return st // not cached so the next turn retries
		}
	}
	staticTokenCache.Store(key, st)
	return st
}

// contextCategories breaks the context into System prompt / Tools / Messages /
// Free space. Messages is whatever remains of the real input total, plus the
// completion (it becomes part of the history on the next turn).
func contextCategories(st staticTokens, u Usage, free int) []map[string]any {
	input := u.PromptTokens + u.CacheRead + u.CacheWrite
	system, tools := st.System, st.Tools
	if system+tools > input { // measurement larger than the real input: shrink proportionally
		if sum := system + tools; sum > 0 {
			system, tools = system*input/sum, tools*input/sum
		}
	}
	messages := input - system - tools + u.CompletionTokens

	out := []map[string]any{}
	add := func(name string, tokens int, color string) {
		if tokens > 0 {
			out = append(out, map[string]any{"name": name, "tokens": tokens, "color": color})
		}
	}
	add("System prompt", system, "promptBorder")
	add("Tools", tools, "tools")
	add("Messages", messages, "purple_FOR_SUBAGENTS_ONLY")
	add("Free space", free, "inactive")
	return out
}
