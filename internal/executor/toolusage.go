package executor

import (
	"encoding/json"
	"unicode/utf8"
)

// toolTracker watches the stdout of one claude process and reports, besides the turn
// totals, what each API call used and how big each tool result was. The hub joins them
// to find out what every tool costs: a result stays in the context and is re-read by every
// later call.
//
// It emits two events:
//   - "api_call" when a call ends (message_delta): its usage and the tools it asked for.
//   - "tool_result" when claude hands a tool's result back: its size, never its content.
//
// The usage of a call is the one of its message_start (what that call billed as input and
// cache) plus the output_tokens of its message_delta.
type toolTracker struct {
	newTurn bool // the next call is the first of a turn
	cur     *trackedCall
}

type trackedCall struct {
	parent        string // parent_tool_use_id: set for the calls of a sub-agent
	model         string
	input, cc, cr int
	cc1h, cc5m    int
	tools         []map[string]any
}

func newToolTracker() *toolTracker { return &toolTracker{newTurn: true} }

// observe reads one stdout line and returns the events it produces (usually none).
func (t *toolTracker) observe(sessID string, line []byte) []map[string]any {
	var top struct {
		Type            string          `json:"type"`
		ParentToolUseID string          `json:"parent_tool_use_id"`
		Event           json.RawMessage `json:"event"`
		Message         json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(line, &top); err != nil {
		return nil
	}
	switch top.Type {
	case "result":
		t.newTurn = true
		t.cur = nil
	case "stream_event":
		return t.stream(sessID, top.ParentToolUseID, top.Event)
	case "user":
		return toolResultEvents(sessID, top.ParentToolUseID, top.Message)
	}
	return nil
}

func (t *toolTracker) stream(sessID, parent string, raw json.RawMessage) []map[string]any {
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Model string `json:"model"`
			Usage struct {
				Input         int `json:"input_tokens"`
				CacheCreation int `json:"cache_creation_input_tokens"`
				CacheRead     int `json:"cache_read_input_tokens"`
				Split         struct {
					OneHour  int `json:"ephemeral_1h_input_tokens"`
					FiveMins int `json:"ephemeral_5m_input_tokens"`
				} `json:"cache_creation"`
			} `json:"usage"`
		} `json:"message"`
		ContentBlock struct {
			Type string `json:"type"`
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content_block"`
		Usage struct {
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil
	}
	switch ev.Type {
	case "message_start":
		u := ev.Message.Usage
		t.cur = &trackedCall{
			parent: parent, model: ev.Message.Model,
			input: u.Input, cc: u.CacheCreation, cr: u.CacheRead,
			cc1h: u.Split.OneHour, cc5m: u.Split.FiveMins,
		}
	case "content_block_start":
		if t.cur != nil && ev.ContentBlock.Type == "tool_use" {
			t.cur.tools = append(t.cur.tools, map[string]any{"id": ev.ContentBlock.ID, "name": ev.ContentBlock.Name})
		}
	case "message_delta":
		c := t.cur
		if c == nil {
			return nil
		}
		t.cur = nil
		first := t.newTurn
		t.newTurn = false
		tools := c.tools
		if tools == nil {
			tools = []map[string]any{}
		}
		return []map[string]any{{
			"type":                        "api_call",
			"session_id":                  sessID,
			"parent_tool_use_id":          c.parent,
			"first_in_turn":               first && c.parent == "",
			"model":                       c.model,
			"input_tokens":                c.input,
			"output_tokens":               ev.Usage.Output,
			"cache_creation_input_tokens": c.cc,
			"cache_read_input_tokens":     c.cr,
			"cache_creation_1h_tokens":    c.cc1h,
			"cache_creation_5m_tokens":    c.cc5m,
			"tools":                       tools,
		}}
	}
	return nil
}

// toolResultEvents turns the tool_result blocks of a user message into size events.
func toolResultEvents(sessID, parent string, rawMsg json.RawMessage) []map[string]any {
	var msg struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(rawMsg, &msg); err != nil {
		return nil
	}
	var blocks []struct {
		Type      string          `json:"type"`
		ToolUseID string          `json:"tool_use_id"`
		Content   json.RawMessage `json:"content"`
		IsError   bool            `json:"is_error"`
	}
	if err := json.Unmarshal(msg.Content, &blocks); err != nil {
		return nil // a plain string: a user message, not a tool result
	}
	var out []map[string]any
	for _, b := range blocks {
		if b.Type != "tool_result" || b.ToolUseID == "" {
			continue
		}
		chars, images := resultSize(b.Content)
		out = append(out, map[string]any{
			"type":               "tool_result",
			"session_id":         sessID,
			"parent_tool_use_id": parent,
			"tool_use_id":        b.ToolUseID,
			"chars":              chars,
			"images":             images,
			"is_error":           b.IsError,
		})
	}
	return out
}

// resultSize counts the characters of the text of a tool result and its images. The
// content is a string or a list of blocks.
func resultSize(raw json.RawMessage) (chars, images int) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return utf8.RuneCountInString(s), 0
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return 0, 0
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			chars += utf8.RuneCountInString(b.Text)
		case "image":
			images++
		}
	}
	return chars, images
}
