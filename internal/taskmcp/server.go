// Package taskmcp is a minimal MCP (Model Context Protocol) server over stdio
// that gives an agent the lucode tools: three to report on its task
// (task_comment, task_done, task_blocked) and two to work with other agents
// (agent_list, send_to_agent) and one to look up tasks (task_info). It is started by `claude -p` through
// --mcp-config; each call is forwarded to lucode by the supplied callback.
package taskmcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// Tool names as the agent sees them (claude prefixes them with mcp__lucode__).
const (
	ToolComment = "task_comment"
	ToolDone    = "task_done"
	ToolBlocked = "task_blocked"

	ToolAgentList   = "agent_list"
	ToolSendToAgent = "send_to_agent"
	ToolTaskInfo    = "task_info"

	// ToolGetTaskImage returns a picture attached to a task (referenced as @imageN in its description).
	ToolGetTaskImage = "get_task_image"
)

// imagePrefix marks a tool result that is a picture, not text: the callback
// returns ImageResult(...) and the server turns it into an MCP image block.
const imagePrefix = "lucode-image:"

// ImageResult encodes a picture (base64 data) as a tool result.
func ImageResult(mediaType, b64 string) string {
	return imagePrefix + mediaType + "\n" + b64
}

// content turns a tool result into MCP content blocks.
func content(text string) []map[string]any {
	if rest, ok := strings.CutPrefix(text, imagePrefix); ok {
		if mime, data, ok := strings.Cut(rest, "\n"); ok && mime != "" && data != "" {
			return []map[string]any{{"type": "image", "mimeType": mime, "data": data}}
		}
	}
	return []map[string]any{{"type": "text", "text": text}}
}

// TaskInfoFields are the fields task_info can return. With none requested it
// returns title, description, status and agent.
var TaskInfoFields = []string{"title", "description", "status", "type", "tags", "agent", "dependencies", "parent", "dates", "comments"}

// Names lists every lucode tool, in display order.
func Names() []string {
	return []string{ToolComment, ToolDone, ToolBlocked, ToolAgentList, ToolSendToAgent, ToolTaskInfo, ToolGetTaskImage}
}

// IsTaskTool reports whether name is one of the task tools, with or without
// the mcp__lucode__ prefix claude adds.
func IsTaskTool(name string) bool {
	switch strings.TrimPrefix(name, "mcp__lucode__") {
	case ToolComment, ToolDone, ToolBlocked, ToolAgentList, ToolSendToAgent, ToolTaskInfo, ToolGetTaskImage:
		return true
	}
	return false
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func textArg(name, desc string) map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{name: map[string]any{"type": "string", "description": desc}},
		"required":   []string{name},
	}
}

var tools = []tool{
	{
		Name:        ToolAgentList,
		Description: "List the other agents of this project: what each one does and whether it is working on a task right now. Use it to find who to ask for help.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name: ToolSendToAgent,
		Description: "Send a message to another agent. lucode adds the envelope (who it is from, which task it is about, whether an answer is expected) and delivers it into the task that agent is working on; its answer comes back later as a message to you, so do not wait for it. " +
			"The agent must have a task in progress. Use to=\"supervisor\" to reach the agent you report to.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to":              map[string]any{"type": "string", "description": "Name of the agent, or \"supervisor\"."},
				"message":         map[string]any{"type": "string", "description": "What you need or want to tell it. Short and specific."},
				"task_id":         map[string]any{"type": "string", "description": "The task this message is about (shown to the recipient as the context). Defaults to your own task."},
				"requires_answer": map[string]any{"type": "boolean", "description": "true if you need a reply from the recipient. Default false."},
			},
			"required": []string{"to", "message"},
		},
	},
	{
		Name: ToolTaskInfo,
		Description: "Look up a task of this project when you need a fact you do not already have. Returns only the fields you ask for (default: title and status), so ask for the fewest that answer your question: " +
			"for example fields=[\"status\"] to tell the user whether a task is done. Do not call it for your own task (it is in your first message) nor just to read the title shown in a message's #Context. Ask for description only when you must understand the work itself.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "Id of the task. Defaults to your own task."},
				"fields": map[string]any{
					"type":        "array",
					"description": "Which fields to return. Ask for as few as possible.",
					"items":       map[string]any{"type": "string", "enum": TaskInfoFields},
				},
			},
		},
	},
	{
		Name: ToolGetTaskImage,
		Description: "Get a picture attached to a task. A task description refers to its pictures as @image1, @image2... (a line at the top of the task message lists them). " +
			"Call it when you need to look at one; the picture comes back as an image.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "Id of the task. Defaults to your own task."},
				"name":    map[string]any{"type": "string", "description": "Name of the picture, e.g. \"image1\" (the @ is optional)."},
			},
			"required": []string{"name"},
		},
	},
	{
		Name:        ToolComment,
		Description: "Post a progress note on your task so the user can follow along. Does not change the task status.",
		InputSchema: textArg("text", "The note, in plain text."),
	},
	{
		Name:        ToolDone,
		Description: "Mark your task as completed. Call it once the work is finished and verified, with a short summary of what you did. It moves the task to Done and unblocks tasks that depend on it.",
		InputSchema: textArg("summary", "What you did and where the result is."),
	},
	{
		Name:        ToolBlocked,
		Description: "Mark your task as blocked because you cannot continue without help from the user. It moves the task to Blocked.",
		InputSchema: textArg("reason", "What is blocking you and what you need."),
	},
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Run serves MCP requests (newline-delimited JSON-RPC) from r to w until r
// closes. call executes a tool and returns the text shown to the agent.
func Run(r io.Reader, w io.Writer, call func(tool string, args json.RawMessage) (string, error)) error {
	return RunWithTools(r, w, call, nil)
}

// RunWithTools is Run limited to the given tools: it lists only those and
// refuses to run any other. nil means all of them; an empty slice means none.
func RunWithTools(r io.Reader, w io.Writer, call func(tool string, args json.RawMessage) (string, error), allowed []string) error {
	enabled := func(name string) bool {
		if allowed == nil {
			return IsTaskTool(name)
		}
		for _, a := range allowed {
			if a == name {
				return true
			}
		}
		return false
	}
	offered := make([]tool, 0, len(tools)) // never nil: an empty list must encode as [], not null
	for _, t := range tools {
		if enabled(t.Name) {
			offered = append(offered, t)
		}
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	enc := json.NewEncoder(w)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}
		if len(req.ID) == 0 { // notification (e.g. notifications/initialized): no reply
			continue
		}
		resp := response{JSONRPC: "2.0", ID: req.ID}

		switch req.Method {
		case "initialize":
			var p struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.ProtocolVersion == "" {
				p.ProtocolVersion = "2024-11-05"
			}
			resp.Result = map[string]any{
				"protocolVersion": p.ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "lucode", "version": "1.0.0"},
			}
		case "ping":
			resp.Result = map[string]any{}
		case "tools/list":
			resp.Result = map[string]any{"tools": offered}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil || !enabled(p.Name) {
				resp.Error = &rpcError{Code: -32602, Message: fmt.Sprintf("unknown tool %q", p.Name)}
				break
			}
			text, err := call(p.Name, p.Arguments)
			result := map[string]any{"content": content(text)}
			if err != nil {
				result["content"] = []map[string]any{{"type": "text", "text": err.Error()}}
				result["isError"] = true
			}
			resp.Result = result
		default:
			resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
		}

		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}
