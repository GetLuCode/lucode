package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// Task and teammate tools: how an agent reports on the lucode task it works on
// (task_comment / task_done / task_blocked) and works with other agents
// (agent_list / send_to_agent / task_info). They mirror the MCP tools lucode gives
// `claude -p` and are forwarded to lucode over NATS, which acts on them.

func init() {
	textTool := func(name, desc, arg, argDesc string) Tool {
		return Tool{
			Type: "function",
			Function: ToolFunction{
				Name:        name,
				Description: desc,
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						arg: map[string]interface{}{"type": "string", "description": argDesc},
					},
					"required": []string{arg},
				},
			},
		}
	}
	Tools = append(Tools,
		textTool("task_comment", "Post a short progress note on your task so the user can follow along. Does not change the task status.", "text", "The note, in plain text."),
		textTool("task_done", "Mark your task as completed, only once the work is finished and verified. Moves it to Done and unblocks dependent tasks.", "summary", "What you did and where the result is."),
		textTool("task_blocked", "Mark your task as blocked because you cannot continue without help from the user. Moves it to Blocked.", "reason", "What blocks you and what you need."),
		Tool{
			Type: "function",
			Function: ToolFunction{
				Name:        "agent_list",
				Description: "List the other agents of this project: what each one does and whether it is working on a task right now.",
				Parameters:  map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
			},
		},
		Tool{
			Type: "function",
			Function: ToolFunction{
				Name: "send_to_agent",
				Description: "Send a message to another agent. lucode adds the envelope (who it is from, which task it is about, whether an answer is expected) and delivers it into the task that agent is working on; its answer comes back later as a message to you, so do not wait for it. " +
					"The agent must have a task in progress. Use to=\"supervisor\" to reach the agent you report to.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"to":              map[string]interface{}{"type": "string", "description": "Name of the agent, or \"supervisor\"."},
						"message":         map[string]interface{}{"type": "string", "description": "What you need or want to tell it. Short and specific."},
						"task_id":         map[string]interface{}{"type": "string", "description": "The task this message is about (shown to the recipient as the context). Defaults to your own task."},
						"requires_answer": map[string]interface{}{"type": "boolean", "description": "true if you need a reply from the recipient. Default false."},
					},
					"required": []string{"to", "message"},
				},
			},
		},
		Tool{
			Type: "function",
			Function: ToolFunction{
				Name: "task_info",
				Description: "Look up a task of this project when you need a fact you do not already have. Returns only the fields you ask for (default: title and status), so ask for the fewest that answer your question: " +
					"for example fields=[\"status\"] to tell the user whether a task is done. Do not call it for your own task (it is in your first message) nor just to read the title shown in a message's #Context. Ask for description only when you must understand the work itself.",
				Parameters: map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"task_id": map[string]interface{}{"type": "string", "description": "Id of the task. Defaults to your own task."},
						"fields": map[string]interface{}{
							"type":        "array",
							"description": "Which fields to return. Ask for as few as possible.",
							"items": map[string]interface{}{
								"type": "string",
								"enum": []string{"title", "description", "status", "type", "tags", "agent", "dependencies", "parent", "dates", "comments"},
							},
						},
					},
				},
			},
		},
	)
}

func isTaskTool(name string) bool {
	switch name {
	case "task_comment", "task_done", "task_blocked", "agent_list", "send_to_agent", "task_info":
		return true
	}
	return false
}

// callTaskTool forwards a task tool call to lucode (request/reply on
// lucode.tasktools.<session>) and returns the text the model should see.
func callTaskTool(ctx context.Context, perm *PermissionClient, name string, args json.RawMessage) string {
	if perm == nil || perm.NC == nil {
		return "Task tools are unavailable: this session is not connected to lucode."
	}
	payload, _ := json.Marshal(map[string]any{"tool": name, "args": args})
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	msg, err := perm.NC.RequestWithContext(reqCtx, "lucode.tasktools."+perm.SessionID, payload)
	if err != nil {
		return fmt.Sprintf("Task tool failed: lucode did not answer (%v)", err)
	}
	var reply struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		return "Task tool failed: unreadable reply from lucode."
	}
	if !reply.OK {
		return "Task tool failed: " + reply.Message
	}
	return reply.Message
}
