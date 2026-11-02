// Package taskmcp is a minimal MCP (Model Context Protocol) server over stdio
// that gives an agent the lucode tools: three to report on its task
// (task_comment, task_done, task_blocked) and two to work with other agents
// (agent_list, agent_message). It is started by `claude -p` through
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

	ToolAgentList    = "agent_list"
	ToolAgentMessage = "agent_message"
)

// IsTaskTool reports whether name is one of the task tools, with or without
// the mcp__lucode__ prefix claude adds.
func IsTaskTool(name string) bool {
	switch strings.TrimPrefix(name, "mcp__lucode__") {
	case ToolComment, ToolDone, ToolBlocked, ToolAgentList, ToolAgentMessage:
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
		Name: ToolAgentMessage,
		Description: "Send a message to another agent. It arrives in the task that agent is working on, and its answer comes back later as a message to you, so do not wait for it. " +
			"The agent must have a task in progress. Use to=\"supervisor\" to reach the agent you report to.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"to":      map[string]any{"type": "string", "description": "Name of the agent, or \"supervisor\"."},
				"message": map[string]any{"type": "string", "description": "What you need or want to tell it. Short and specific."},
			},
			"required": []string{"to", "message"},
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
			resp.Result = map[string]any{"tools": tools}
		case "tools/call":
			var p struct {
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &p); err != nil || !IsTaskTool(p.Name) {
				resp.Error = &rpcError{Code: -32602, Message: fmt.Sprintf("unknown tool %q", p.Name)}
				break
			}
			text, err := call(p.Name, p.Arguments)
			result := map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
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
