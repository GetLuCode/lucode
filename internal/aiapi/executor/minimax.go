package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"claude-orchestrator/internal/aiapi/executor/checkpoint"
	"claude-orchestrator/internal/aiapi/executor/worktree"
)

// textContent extracts the plain string from a ChatMessage's Content field.
// Content is json.RawMessage because the user-message side of the wire may
// carry an array of content parts (text + image_url). For everything we
// accumulate or display (assistant text, tool output, history replay), the
// payload is a JSON-quoted string — we just unquote it.
func textContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	// Fallback: it might be a number, bool, etc. — return as-is string.
	return string(raw)
}

// DefaultMiniMaxBaseURL is the public MiniMax API endpoint. Configurable via
// the --minimax-base-url flag.
const DefaultMiniMaxBaseURL = "http://localhost:3100/anthropic"

// MaxAgenticRounds caps how many tool-call → result → call cycles we'll do
// per turn. The loop exits on the first finish_reason=stop or after this many
// rounds, whichever comes first.
const MaxAgenticRounds = 10

// systemPrompt is prepended to every turn so the model knows the runtime
// rules it must follow when calling tools. Kept here (not in each tool's
// description) because most of it is cross-cutting and would otherwise be
// duplicated 6 times.
//
// Uses var (not const) so the MaxAgenticRounds count stays in sync if the
// constant is ever tuned.
var systemPrompt = `You have access to a small set of tools that operate on the local filesystem and run shell commands. The runtime enforces these rules — read them before calling a tool.

Working directory
- Every tool that takes a "path" arg resolves it against the current working directory (the first working dir passed in by the orchestrator). An empty path defaults to the working directory itself.
- Paths that escape the working directory are rejected — including platform-foreign absolute paths (e.g. "/etc/passwd" on Windows, or "C:\\foo" on Linux). If you hit a path guard error, your path is wrong; do not retry with a different escape attempt.

Response format
- read_file, view, list_directory, grep, and filesystem return a JSON object: {"head_checkpoint": "<sha256>", "content": "..."} on success, or {"head_checkpoint": "<sha256>", "error": "error: <reason>"} on failure. Parse the JSON to extract the payload.
- run_command is the exception: it returns a plain string (no JSON, no head). Its effects are not tracked by checkpoints.

Head checkpoints (optimistic concurrency)
- Every read tool returns a "head_checkpoint" id — a sha256 of the current tracked file state. When you call filesystem to edit a file you just read, pass that same id as "expected_head_checkpoint". If the workDir changed in the meantime (e.g. another tool, a revert in the UI, or an external edit), the call is rejected with "stale head checkpoint". Retry by re-reading.
- Omit expected_head_checkpoint on the first filesystem call of a turn, or when the change is independent of any prior read.

Workspace model (worktree-based isolation)
- When you call filesystem for the first time in a turn, the runtime creates a private scratch directory (a git worktree) for your edits. All subsequent filesystem calls in the same turn target that scratch dir, not the user's actual working dir.
- This means you can edit freely without worrying about clobbering the user's uncommitted changes — those live in a separate place and the runtime merges your work in at the end of the turn.
- At end of turn, the runtime runs a 3-way merge: it compares your worktree against the user's committed HEAD and the user's current working dir. Non-conflicting changes are applied automatically.
- If there ARE conflicts (you and the user changed the same file in incompatible ways), you'll receive a follow-up turn with a synthetic prompt containing the conflicted files (with <<<<<<</=======/>>>>>>> markers). Just edit the files to resolve the markers, and the runtime will re-merge.
- You don't need to do anything special for this to work — just edit files. The runtime handles the worktree setup, the merge, and the conflict resolution loop.

Execution model
- Tools run sequentially, one per round. You see the result of each call before deciding the next.
- The loop is capped at ` + fmt.Sprintf("%d", MaxAgenticRounds) + ` tool-call rounds per turn. After that, the turn ends with whatever has been emitted so far. Plan multi-step work accordingly and prefer one batched filesystem call over many tiny ones.

File modifications
- ALL file modifications go through the "filesystem" tool — there is no write_file or edit_file. Pass an "ops" array of one or more of: create, update, delete, rename, copy. Multiple ops in one call are atomic: if any fails, NONE are applied.
- For "update": old_string is an exact substring match (no regex). If it appears multiple times, the call fails — either include surrounding context for uniqueness, or pass "replace_all": true to replace every occurrence, or pass "occurrence_index": N (1-indexed) to replace the Nth match. Use grep first to count matches if you're unsure.
- The file is left UNCHANGED on any error (validation or runtime). If a filesystem call returns an error, no ops were applied.
- Each successful filesystem call creates a persistent checkpoint in <workDir>/.lucode/checkpoints/ that the user can revert to from the UI. The checkpoint is automatic — you don't need to do anything. You will NOT see checkpoint ids in the tool output (the head id is the only user-visible state); the user sees the history in their UI. If you want to be helpful, you can describe what you just changed in your reply so the user knows what the checkpoint is for.

Output limits
- read_file is capped at 256 KB; view is capped at 500 lines per call; list_directory at 500 entries; run_command output at 8 KB; grep at 1000 matches. When a result is truncated, the trailing line tells you the true size — use view or grep to inspect selectively.
- run_command runs via "sh -c" with cwd set to the working directory. On Windows, sh.exe must be on PATH (e.g. via Git Bash). The command has no timeout, no interactive input, and no undo. Its effects are NOT tracked by checkpoints.

Ignored paths
- A built-in ignore list (node_modules, dist, .git, .lucode, etc.) plus an optional .agentignore file in the workDir mark paths that are NOT tracked by the head checkpoint. You can still read them with read_file/view, but if you modify them, the change will not be visible in future head comparisons and is not revertable via the checkpoint system. Use run_command or filesystem ops for source code; treat ignored paths as ephemeral.`

// Client is the MiniMax HTTP client. Stateless — safe to share across turns.
type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a configured MiniMax client.
func NewClient(apiKey, baseURL string) *Client {
	if baseURL == "" {
		baseURL = DefaultMiniMaxBaseURL
	}
	return &Client{
		APIKey:  apiKey,
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
}

// understandImage runs the MiniMax MCP understand_image tool on the given
// base64 image and returns a textual description. It requires:
//   - MINIMAX_API_KEY env var set to the API key
//   - MINIMAX_API_HOST env var set to the API host (matches DefaultMiniMaxBaseURL)
//   - uvx available in PATH
//
// If the MCP call fails, it logs the error and returns an empty string.
// The caller should fall back to passing the image directly (if the model
// supports it) or responding with an error message.
func understandImage(ctx context.Context, apiKey, baseURL, imageBase64, imageMediaType string) string {
	if apiKey == "" {
		log.Printf("[minimax] understandImage: no API key provided")
		return ""
	}

	// Determine the file extension from the media type.
	ext := ".png"
	switch imageMediaType {
	case "image/jpeg", "image/jpg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}

	// Write the image to a temp file.
	tmpDir := os.TempDir()
	tmpFile := filepath.Join(tmpDir, "minimax-img-"+fmt.Sprintf("%d", time.Now().UnixNano())) + ext
	data, err := base64.StdEncoding.DecodeString(imageBase64)
	if err != nil {
		log.Printf("[minimax] understandImage: base64 decode: %v", err)
		return ""
	}
	if err := os.WriteFile(tmpFile, data, 0600); err != nil {
		log.Printf("[minimax] understandImage: write temp file: %v", err)
		return ""
	}
	defer os.Remove(tmpFile)
	log.Printf("[minimax] understandImage: temp file=%s", tmpFile)

	// Build the prompt we want the model to answer about the image.
	prompt := "Describe this image in detail. Focus on: what is shown, any text visible, " +
		"layout or structure, notable colors or visual elements, and any content relevant " +
		"for a coding assistant context (e.g. UI screenshots, diagrams, code on screen)."

	// Build JSON-RPC requests.
	// First: initialize handshake.
	initReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{},
			"clientInfo": map[string]any{
				"name":    "aiapi",
				"version": "1.0",
			},
		},
	}
	initJSON, _ := json.Marshal(initReq)

	// Then: call the understand_image tool.
	callReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "understand_image",
			"arguments": map[string]any{
				"prompt":       prompt,
				"image_source": tmpFile,
			},
		},
	}
	callJSON, _ := json.Marshal(callReq)

	// Run the MCP server over stdio. The MCP server concatenates the
	// host with endpoints that already start with "/v1/coding_plan/...".
	// The chat client uses the Anthropic-compatible base at
	// <origin>/anthropic, but the MCP server expects the bare API origin
	// (no /anthropic suffix), so strip it before passing to the subprocess.
	mcpHost := strings.TrimSuffix(baseURL, "/anthropic")
	mcpHost = strings.TrimRight(mcpHost, "/")
	cmd := exec.CommandContext(ctx, "uvx", "minimax-coding-plan-mcp", "-y")
	cmd.Env = append(os.Environ(),
		"MINIMAX_API_KEY="+apiKey,
		"MINIMAX_API_HOST="+mcpHost,
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("[minimax] understandImage: stdin pipe: %v", err)
		return ""
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("[minimax] understandImage: stdout pipe: %v", err)
		return ""
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		log.Printf("[minimax] understandImage: stderr pipe: %v", err)
		return ""
	}

	if err := cmd.Start(); err != nil {
		log.Printf("[minimax] understandImage: start uvx: %v", err)
		return ""
	}

	// Send initialize then the tool call.
	fmt.Fprintf(stdin, "%s\n", initJSON)
	fmt.Fprintf(stdin, "%s\n", callJSON)
	stdin.Close()

	// Read all stdout and stderr.
	var allStdout, allStderr bytes.Buffer

	// Drain stderr in parallel so the pipe doesn't fill up and deadlock.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		io.Copy(&allStderr, stderr)
		wg.Done()
	}()
	io.Copy(&allStdout, stdout)
	wg.Wait()
	cmd.Wait()

	rawOut := allStdout.String()
	rawErr := allStderr.String()
	log.Printf("[minimax] understandImage: stdout lines=%d stderr_len=%d",
		strings.Count(rawOut, "\n"), len(rawErr))

	if rawErr != "" {
		log.Printf("[minimax] understandImage: stderr: %s", truncateForLog(rawErr))
	}

	// Parse each JSON-RPC response line.
	scanner := bufio.NewScanner(bytes.NewReader(allStdout.Bytes()))
	scanner.Buffer(make([]byte, 0, 256*1024), 512*1024)
	var description string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		log.Printf("[minimax] understandImage: response line: %s", truncateForLog(line))

		// Try to extract description from the response.
		// MCP tools/call returns { result: { content: [{ type: "text", text: "..." }] } }
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		result, ok := raw["result"].(map[string]any)
		if !ok {
			// Check for error response.
			if errObj, ok := raw["error"].(map[string]any); ok {
				log.Printf("[minimax] understandImage: MCP error: %v", errObj)
			}
			continue
		}
		contentArr, ok := result["content"].([]any)
		if !ok || len(contentArr) == 0 {
			continue
		}
		contentItem, ok := contentArr[0].(map[string]any)
		if !ok {
			continue
		}
		if contentItem["type"] == "text" && contentItem["text"] != "" {
			description = contentItem["text"].(string)
			break
		}
	}

	if description == "" {
		log.Printf("[minimax] understandImage: no description extracted. Full output: %s",
			truncateForLog(rawOut))
	}
	return description
}

// Run executes one turn: streams the model response, runs the agentic loop
// until the model stops or we hit MaxAgenticRounds, and emits events to onEvent
// in the same shape the UI already understands:
//
//	{ "type": "session_status",  "status": "running|idle|error" }
//	{ "type": "stream_event",    "event":  <raw chunk> }       // per SSE chunk
//	{ "type": "message_ready",   "message": { "role": "assistant", "content": "...", "thinking": "..." } }
//	{ "type": "turn_usage",      "input_tokens": N, "output_tokens": N, "reasoning_tokens": N, "model": "..." }
//	{ "type": "context_usage",   "totalTokens": N, "maxTokens": N, "percentage": N, "model": "..." }
func (c *Client) Run(ctx context.Context, req TurnRequest, onEvent func(map[string]any), perm *PermissionClient) error {
	if onEvent == nil {
		onEvent = func(map[string]any) {}
	}

	onEvent(map[string]any{
		"type":   "session_status",
		"status": "running",
	})

	// Pre-process image: use MiniMax MCP understand_image if an image is attached.
	// This converts the raw image to a text description that can be included in
	// the user message, since the MiniMax chat completions API does not natively
	// support inline base64 image content.
	message := req.Message
	if _, isClaude := ResolveClaudeModel(req.Model); req.ImageBase64 != "" && !isClaude {
		if desc := understandImage(ctx, c.APIKey, c.BaseURL, req.ImageBase64, req.ImageMediaType); desc != "" {
			message = "Image description: " + desc + "\n\nUser message: " + req.Message
			log.Printf("[minimax] understandImage: got description (%d chars)", len(desc))
			// Surface the description to the UI as a synthetic message so the
			// user can see what the model will see, even though it's not part
			// of the actual conversation history.
			onEvent(map[string]any{
				"type":    "synthetic_message",
				"message": "📎 Image description (from MiniMax VLM):\n\n" + desc,
			})
		} else {
			// MCP failed — still include the original message with a note.
			// The API will reject the image if it truly can't handle it.
			log.Printf("[minimax] understandImage: MCP failed, sending without image description")
			onEvent(map[string]any{
				"type":    "synthetic_message",
				"message": "⚠️ Image attached but MiniMax VLM failed to describe it. The model will only see your text message.",
			})
		}
	}

	messages := buildMessages(req, message)
	workDir := ""
	if len(req.WorkDirs) > 0 {
		workDir = req.WorkDirs[0]
	}

	// Worktree manager isolates the model's filesystem edits from the
	// user's working dir. It's created lazily on the first filesystem
	// call (via wm.Active() check in executeTool) and cleaned up at
	// the end of the turn. In non-git dirs it's a passthrough no-op.
	wm := worktree.NewManager(workDir, req.SessionID)
	defer wm.Cleanup()

	var (
		finalContent  strings.Builder
		finalThinking strings.Builder
		usage         Usage
	)

	for round := 0; round < MaxAgenticRounds; round++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		msg, u, err := c.chatOnce(ctx, req.Model, req.Thinking, toolsForRequest(req), messages, onEvent)
		if err != nil {
			onEvent(map[string]any{
				"type":   "session_status",
				"status": "error",
				"error":  err.Error(),
			})
			return err
		}
		usage = u // each round returns the chunk-final usage; keep the last

		text0, thinking0 := splitThinking(textContent(msg.Content))
		log.Printf("[minimax] round=%d finish=%q tool_calls=%d text_len=%d thinking_len=%d sess=%s",
			round, msg.FinishReason, len(msg.ToolCalls), len(text0), len(thinking0), req.SessionID)

		// Append the assistant's message verbatim to keep the conversation
		// shape required by the API.
		messages = append(messages, msg)

		// Pull the visible text and the thinking block out of the accumulated
		// content so we can show them separately in the UI.
		text, thinking := text0, thinking0
		if thinking != "" {
			finalThinking.WriteString(thinking)
			if finalThinking.Len() > 0 && finalThinking.String() != "" {
				finalThinking.WriteString("\n")
			}
		}
		if text != "" {
			finalContent.WriteString(text)
		}

		// If the model didn't ask to call any tool, we're done.
		if len(msg.ToolCalls) == 0 || msg.FinishReason == "stop" {
			if len(msg.ToolCalls) == 0 && text == "" && thinking != "" {
				log.Printf("[minimax] WARN round=%d: model produced thinking but no text and no tool_calls — "+
					"this usually means the proxy did not send tool_use content blocks. "+
					"Check that the model/proxy supports the Anthropic tools API parameter.", round)
			}
			break
		}

		// Execute each tool call and append a "tool" message with the result.
		for _, tc := range msg.ToolCalls {
			result := runToolWithPermission(ctx, perm, tc, workDir, wm, req, onEvent)
			messages = append(messages, ChatMessage{
				Role:       "tool",
				Content:    JSONQuote(result),
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			})
		}
	}

	// Apply phase: 3-way merge from worktree to user's workDir. If
	// there are conflicts, run a conflict-resolution sub-loop where
	// the model can edit the conflicted files in place.
	if applyErr := runApplyPhase(ctx, c, wm, req, &messages, onEvent, workDir, perm); applyErr != nil {
		log.Printf("[executor] apply phase: %v", applyErr)
	}

	onEvent(map[string]any{
		"type": "message_ready",
		"message": map[string]any{
			"role":     "assistant",
			"content":  strings.TrimSpace(finalContent.String()),
			"thinking": strings.TrimSpace(finalThinking.String()),
		},
	})

	onEvent(map[string]any{
		"type":             "turn_usage",
		"input_tokens":     usage.PromptTokens,
		"output_tokens":    usage.CompletionTokens,
		"reasoning_tokens": usage.ReasoningTokens,

		"cache_read_input_tokens":     usage.CacheRead,
		"cache_creation_input_tokens": usage.CacheWrite,
		"total_tokens":                usage.TotalTokens,
		"model":                       req.Model,
	})

	// Context-usage bar. Anthropic reports cached prompt tokens separately from
	// input_tokens, so all three are summed; the completion is included because
	// it becomes part of the context on the next turn. Claude windows come from
	// ClaudeLimits; 200k is the MiniMax default.
	maxCtx := 200000
	ctxSys, _ := toAnthropicMessages(messages, "")
	if id, ok := ResolveClaudeModel(req.Model); ok {
		maxCtx, _ = ClaudeLimits(id)
		ctxSys = append([]map[string]any{{"type": "text", "text": claudeOAuthPrefix}}, ctxSys...)
	}
	ctxTools := toAnthropicTools(toolsForRequest(req))
	used := usage.PromptTokens + usage.CacheRead + usage.CacheWrite + usage.CompletionTokens
	pct := int(float64(used) / float64(maxCtx) * 100)
	free := maxCtx - used
	if free < 0 {
		free = 0
	}
	onEvent(map[string]any{
		"type":        "context_usage",
		"totalTokens": used,
		"maxTokens":   maxCtx,
		"percentage":  pct,
		"model":       req.Model,
		"categories":  contextCategories(c.staticContextTokens(ctx, req.Model, ctxSys, ctxTools), usage, free),
	})

	onEvent(map[string]any{
		"type":   "session_status",
		"status": "idle",
	})
	return nil
}

// runApplyPhase performs the 3-way merge from the worktree to the
// user's workDir. If there are conflicts, it runs a conflict-resolution
// sub-loop where the model can edit the conflicted files in place
// (this is invisible to the user — the model's edits during this
// phase target the user dir, not the worktree, since the worktree
// has been destroyed).
//
// Returns nil on success (clean merge or successful conflict
// resolution). On any unrecoverable error, returns the error and the
// caller logs it; the turn still completes (with a degraded state).
func runApplyPhase(
	ctx context.Context,
	c *Client,
	wm *worktree.Manager,
	req TurnRequest,
	messages *[]ChatMessage,
	onEvent func(map[string]any),
	workDir string,
	perm *PermissionClient,
) error {
	if wm == nil || !wm.Active() {
		// No worktree = no model changes = nothing to apply.
		return nil
	}

	const maxConflictRounds = 3
	for round := 0; round < maxConflictRounds; round++ {
		result, err := wm.Apply()
		if err != nil {
			return fmt.Errorf("apply: %w", err)
		}

		if len(result.ConflictedFiles) == 0 {
			// Clean merge. Capture a CAS checkpoint of the post-apply
			// state so the user can revert if they don't like the
			// result.
			if workDir != "" {
				store, sErr := checkpoint.NewStore(workDir)
				if sErr == nil {
					summary := fmt.Sprintf("apply model turn: %d clean, %d deleted", len(result.CleanFiles), len(result.DeletedFiles))
					_, _ = store.Snapshot("", nil, summary)
				}
			}
			return nil
		}

		// Conflicts: destroy the worktree so subsequent fsop calls
		// target the user dir. The conflict markers are already in
		// the user dir (wm.Apply wrote them).
		if cErr := wm.Cleanup(); cErr != nil {
			return fmt.Errorf("cleanup after conflicts: %w", cErr)
		}
		log.Printf("[executor] apply phase: %d conflicted files, triggering resolution round %d", len(result.ConflictedFiles), round+1)

		// Synthesize a user message that describes the conflicts and
		// asks the model to resolve them. The model is expected to
		// read the conflicted files and call filesystem to write the
		// resolved versions (without the markers).
		conflictMsg := buildConflictMessage(result.ConflictedFiles)
		*messages = append(*messages, ChatMessage{
			Role:    "user",
			Content: JSONQuote(conflictMsg),
		})
		onEvent(map[string]any{
			"type":    "synthetic_message",
			"message": conflictMsg,
		})

		// Run another agentic loop. The model will see the synthetic
		// message and can call filesystem to fix the conflicts. wm is
		// now inactive (Cleanup was called), so fsop calls target
		// the user dir directly.
		if err := runResolutionLoop(ctx, c, req, *messages, wm, workDir, onEvent, perm); err != nil {
			return fmt.Errorf("resolution loop: %w", err)
		}

		// Check if the model actually resolved the conflicts. If
		// markers remain, loop again (the model will see them again
		// in the next iteration's "stale" detection or the user
		// can intervene manually).
		remaining := scanForConflictMarkers(workDir, result.ConflictedFiles)
		if len(remaining) == 0 {
			// Conflicts resolved by the model. Capture checkpoint.
			if workDir != "" {
				store, sErr := checkpoint.NewStore(workDir)
				if sErr == nil {
					_, _ = store.Snapshot("", nil, fmt.Sprintf("apply model turn: %d files resolved by agent", len(result.ConflictedFiles)))
				}
			}
			return nil
		}
		log.Printf("[executor] apply phase: %d files still have conflict markers after round %d", len(remaining), round+1)
	}

	return fmt.Errorf("max conflict resolution rounds (%d) exceeded", maxConflictRounds)
}

// runResolutionLoop runs the agentic loop until the model stops (after
// responding to the synthetic conflict message) or the round limit
// is hit. Tool calls target the user dir directly (wm is inactive here).
func runResolutionLoop(
	ctx context.Context,
	c *Client,
	req TurnRequest,
	messages []ChatMessage,
	wm *worktree.Manager,
	workDir string,
	onEvent func(map[string]any),
	perm *PermissionClient,
) error {
	for round := 0; round < MaxAgenticRounds; round++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		msg, _, err := c.chatOnce(ctx, req.Model, req.Thinking, toolsForRequest(req), messages, onEvent)
		if err != nil {
			return err
		}
		messages = append(messages, msg)
		if len(msg.ToolCalls) == 0 || msg.FinishReason == "stop" {
			return nil
		}
		for _, tc := range msg.ToolCalls {
			result := runToolWithPermission(ctx, perm, tc, workDir, wm, req, onEvent)
			messages = append(messages, ChatMessage{
				Role:       "tool",
				Content:    JSONQuote(result),
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
			})
		}
	}
	return nil
}

// buildConflictMessage produces the synthetic user message that
// describes a set of conflicted files for the model to resolve.
func buildConflictMessage(conflictedFiles []string) string {
	var sb strings.Builder
	sb.WriteString("Conflict resolution needed.\n\n")
	sb.WriteString("Your previous edits conflicted with the user's uncommitted changes in ")
	sb.WriteString(strconv.Itoa(len(conflictedFiles)))
	sb.WriteString(" file(s). Each file now has git-style conflict markers (<<<<<<<, =======, >>>>>>>).\n\n")
	sb.WriteString("Files:\n")
	for _, p := range conflictedFiles {
		sb.WriteString("  - ")
		sb.WriteString(p)
		sb.WriteByte('\n')
	}
	sb.WriteString("\nFor each file, read it (view or read_file), combine both intentions into a single coherent version, and write it back with filesystem. Remove all conflict markers. When done, the turn will end automatically.\n")
	return sb.String()
}

// scanForConflictMarkers reads each file and checks whether it still
// has conflict markers. Returns the subset that still has them.
func scanForConflictMarkers(workDir string, files []string) []string {
	var remaining []string
	for _, rel := range files {
		abs := filepath.Join(workDir, filepath.FromSlash(rel))
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		if bytes.Contains(data, []byte("<<<<<<<")) ||
			(bytes.Contains(data, []byte("=======")) && bytes.Contains(data, []byte(">>>>>>>"))) {
			remaining = append(remaining, rel)
		}
	}
	return remaining
}

// the SSE response, and returns the fully assembled assistant message plus
// the final usage object.
// toAnthropicMessages converts our internal ChatMessage list to Anthropic's
// shape. The system prompt is sent as a top-level "system" field with a single
// text block; everything else is a list of role/content messages. User messages
// may carry an image as a base64 source block; tool results are tool_result blocks.
func toAnthropicMessages(messages []ChatMessage, systemPrompt string) ([]map[string]any, []map[string]any) {
	var sysBlocks []map[string]any
	if systemPrompt != "" {
		sysBlocks = append(sysBlocks, map[string]any{"type": "text", "text": systemPrompt, "cache_control": map[string]any{"type": "ephemeral"}})
	}
	/*
			{
		      "type": "text",
		      "text": "\nYou are an interactive agent that helps users with software engineering tasks.\n\nIMPORTANT: Assist with authorized security testing, defensive security, CTF challenges, and educational contexts. Refuse requests for destructive techniques, DoS attacks, mass targeting, supply chain compromise, or detection evasion for malicious purposes. Dual-use security tools (C2 frameworks, credential testing, exploit development) require clear authorization context: pentesting engagements, CTF competitions, security research, or defensive use cases.\n\n# Harness\n - Text you output outside of tool use is displayed to the user as Github-flavored markdown in a terminal.\n - Tools run behind a user-selected permission mode; a denied call means the user declined it — adjust, don't retry verbatim.\n - `<system-reminder>` tags in messages and tool results are injected by the harness, not the user. Hooks may intercept tool calls; treat hook output as user feedback.\n - Prefer the dedicated file/search tools over shell commands when one fits. Independent tool calls can run in parallel in one response.\n - Reference code as `file_path:line_number` — it's clickable.\n\nWrite code that reads like the surrounding code: match its comment density, naming, and idiom.\n\nFor actions that are hard to reverse or outward-facing, confirm first unless durably authorized or explicitly told to proceed without asking; approval in one context doesn't extend to the next. Sending content to an external service publishes it; it may be cached or indexed even if later deleted. Before deleting or overwriting, look at the target — if what you find contradicts how it was described, or you didn't create it, surface that instead of proceeding. Report outcomes faithfully: if tests fail, say so with the output; if a step was skipped, say that; when something is done and verified, state it plainly without hedging.\n\n# Session-specific guidance\n - If you need the user to run a shell command themselves (e.g., an interactive login like `gcloud auth login`), suggest they type `! <command>` in the prompt — the `!` prefix runs the command in this session so its output lands directly in the conversation.\n - When the user types `/<skill-name>`, invoke it via Skill. Only use skills listed in the user-invocable skills section — don't guess.\n\n# Memory\n\nYou have a persistent file-based memory at `C:\\Users\\m4rk\\.claude\\projects\\C--projects-terminator\\memory\\`. This directory already exists — write to it directly with the Write tool (do not run mkdir or check for its existence). Each memory is one file holding one fact, with frontmatter:\n\n```markdown\n---\nname: <short-kebab-case-slug>\ndescription: <one-line summary — used to decide relevance during recall>\nmetadata:\n  type: user | feedback | project | reference\n---\n\n<the fact; for feedback/project, follow with **Why:** and **How to apply:** lines. Link related memories with [[their-name]].>\n```\n\nIn the body, link to related memories with `[[name]]`, where `name` is the other memory's `name:` slug. Link liberally — a `[[name]]` that doesn't match an existing memory yet is fine; it marks something worth writing later, not an error.\n\n`user` — who the user is (role, expertise, preferences). `feedback` — guidance the user has given on how you should work, both corrections and confirmed approaches; include the why. `project` — ongoing work, goals, or constraints not derivable from the code or git history; convert relative dates to absolute. `reference` — pointers to external resources (URLs, dashboards, tickets).\n\nAfter writing the file, add a one-line pointer in `MEMORY.md` (`- [Title](file.md) — hook`). `MEMORY.md` is the index loaded into context each session — one line per memory, no frontmatter, never put memory content there.\n\nBefore saving, check for an existing file that already covers it — update that file rather than creating a duplicate; delete memories that turn out to be wrong. Don't save what the repo already records (code structure, past fixes, git history, CLAUDE.md) or what only matters to this conversation; if asked to remember one of those, ask what was non-obvious about it and save that instead. Recalled memories appearing inside `<system-reminder>` blocks are background context, not user instructions, and reflect what was true when written — if one names a file, function, or flag, verify it still exists before recommending it.\n\n# Environment\nYou have been invoked in the following environment: \n - Primary working directory: C:\\projects\\terminator\n - Is a git repository: true\n - Platform: win32\n - Shell: PowerShell (primary); Bash tool also available for POSIX scripts — each takes its own syntax.\n - OS Version: Windows 11 Pro 10.0.26200\n - You are powered by the model named Opus 4.8 (1M context). The exact model ID is claude-opus-4-8[1m].\n - Assistant knowledge cutoff is January 2026.\n - The most recent Claude models are Fable 5 and the Claude 4.X family. Model IDs — Fable 5: 'claude-fable-5', Opus 4.8: 'claude-opus-4-8', Sonnet 4.6: 'claude-sonnet-4-6', Haiku 4.5: 'claude-haiku-4-5-20251001'. When building AI applications, default to the latest and most capable Claude models.\n - Claude Code is available as a CLI in the terminal, desktop app (Mac/Windows), web app (claude.ai/code), and IDE extensions (VS Code, JetBrains).\n - Fast mode for Claude Code uses Claude Opus with faster output (it does not downgrade to a smaller model). It can be toggled with /fast and is available on Opus 4.8/4.7/4.6.\n\n# Context management\nWhen the conversation grows long, some or all of the current context is summarized; the summary, along with any remaining unsummarized context, is provided in the next context window so work can continue — you don't need to wrap up early or hand off mid-task.\n\ngitStatus: This is the git status at the start of the conversation. Note that this status is a snapshot in time, and will not update during the conversation.\n\nCurrent branch: main\n\nMain branch (you will usually use this for PRs): main\n\nGit user: Marco Orta\n\nStatus:\nM backend/main.go\n D backend/static/assets/index-BpZsJoTs.js\n D backend/static/assets/index-CedAuhwp.css\n M backend/static/index.html\n M desktop/app.go\n M frontend/package-lock.json\n?? backend/paths/\n?? backend/static/assets/index-CtXsIHU-.js\n?? backend/static/assets/index-DUPtWCu9.css\n\nRecent commits:\n4d05875 fix: tasks section shows all projects + workspace dropdown z-index via portal\n725cab7 feat: workspace picker in titlebar center + run tasks feature\n209b10f feat: terminal styles, project content view, workspace scoping\nc57c469 fix: window drag on Windows via native WM_NCLBUTTONDOWN\n915690e feat: full-UI transparency + xterm allowTransparency + titlebar drag",
		      "cache_control": {
		        "type": "ephemeral"
		      }
		    }
	*/
	// Single cache breakpoint: the last user or tool message, whichever comes later.
	lastCacheIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" || messages[i].Role == "tool" {
			lastCacheIdx = i
			break
		}
	}

	out := make([]map[string]any, 0, len(messages))
	for i, m := range messages {
		role := m.Role
		switch role {
		case "system":
			// Anthropic has no system role in messages; the system field is top-level.
			txt := textContent(m.Content)
			if txt != "" {
				sysBlocks = append(sysBlocks, map[string]any{"type": "text", "text": txt, "cache_control": map[string]any{"type": "ephemeral"}})
			}
			continue
		case "tool":
			msg := map[string]any{
				"role": "user",
				"content": []map[string]any{{
					"type":        "tool_result",
					"tool_use_id": m.ToolCallID,
					"content":     textContent(m.Content),
				}},
			}
			if i == lastCacheIdx {
				// cache_control must live on a content block, not on the message.
				msg["content"].([]map[string]any)[0]["cache_control"] = map[string]any{"type": "ephemeral"}
			}
			out = append(out, msg)
			continue
		case "assistant":
			visibleText, _ := splitThinking(textContent(m.Content))
			if len(m.ToolCalls) > 0 {
				blocks := []map[string]any{}
				if visibleText != "" {
					blocks = append(blocks, map[string]any{"type": "text", "text": visibleText})
				}
				for _, tc := range m.ToolCalls {
					blocks = append(blocks, map[string]any{
						"type":  "tool_use",
						"id":    tc.ID,
						"name":  tc.Function.Name,
						"input": json.RawMessage(tc.Function.Arguments),
					})
				}
				out = append(out, map[string]any{"role": "assistant", "content": blocks})
				continue
			}
			// Plain text assistant message.
			out = append(out, map[string]any{
				"role":    "assistant",
				"content": visibleText,
			})
			continue
		default: // "user" and any future roles
			txt := textContent(m.Content)
			if txt == "" {
				continue
			}
			if i == lastCacheIdx {
				out = append(out, map[string]any{
					"role": "user",
					"content": []map[string]any{{
						"type":          "text",
						"text":          txt,
						"cache_control": map[string]any{"type": "ephemeral"},
					}},
				})
			} else {
				out = append(out, map[string]any{
					"role":    "user",
					"content": txt,
				})
			}
		}
	}
	return sysBlocks, out
}

// toAnthropicTools converts our internal Tool list to Anthropic's shape.
// cache_control is placed on the last tool so the entire tools block is cached
// — tools never change between turns, so it will always be a cache hit after
// the first write.
func toAnthropicTools(tools []Tool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for i, t := range tools {
		entry := map[string]any{
			"name":         t.Function.Name,
			"description":  t.Function.Description,
			"input_schema": t.Function.Parameters,
		}
		if i == len(tools)-1 {
			entry["cache_control"] = map[string]any{"type": "ephemeral"}
		}
		out = append(out, entry)
	}
	return out
}

// maxTokensForModel returns the appropriate max_tokens limit per model.
func maxTokensForModel(model string) int {
	if strings.Contains(model, "M3") {
		return 131072
	}
	return 65536 // MiniMax-M2.7
}

// thinkingForModel returns the thinking config block to include in the request,
// or nil when the model does not support configurable thinking (M2.7 always
// runs with thinking on and has no thinking parameter).
func thinkingForModel(model, thinking string) map[string]any {
	if !strings.Contains(model, "M3") {
		return nil
	}
	t := thinking
	if t != "disabled" {
		t = "adaptive"
	}
	return map[string]any{"type": t}
}

// chatOnce posts a single Anthropic Messages request and parses the SSE
// stream. Returns the assembled assistant message and the final usage block.
func (c *Client) chatOnce(ctx context.Context, model, thinking string, tools []Tool, messages []ChatMessage, onEvent func(map[string]any)) (ChatMessage, Usage, error) {
	sysBlocks, anthropicMsgs := toAnthropicMessages(messages, "") // system prompt is already in messages[0] from buildMessages

	claudeID, isClaude := ResolveClaudeModel(model)
	maxTokens := maxTokensForModel(model)
	if isClaude {
		model = claudeID
		_, maxTokens = ClaudeLimits(model)
		// OAuth tokens are only accepted with the Claude Code identity prefix.
		sysBlocks = append([]map[string]any{{"type": "text", "text": claudeOAuthPrefix}}, sysBlocks...)
	}

	body := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"stream":     true,
		"messages":   anthropicMsgs,
		"tools":      toAnthropicTools(tools),
	}
	if len(sysBlocks) > 0 {
		body["system"] = sysBlocks
	}
	if isClaude {
		th, oc := claudeThinking(model, thinking)
		body["thinking"] = th
		if oc != nil {
			body["output_config"] = oc
		}
	} else if th := thinkingForModel(model, thinking); th != nil {
		body["thinking"] = th
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return ChatMessage{}, Usage{}, fmt.Errorf("marshal request: %w", err)
	}

	baseURL := c.BaseURL
	if isClaude {
		baseURL = claudeBaseURL
	}
	url := strings.TrimRight(baseURL, "/") + "/v1/messages"

	// send builds and posts the request. For Claude the OAuth token is fetched
	// per attempt so a 401 can be retried once with a forced refresh.
	send := func(forceRefresh bool) (*http.Response, error) {
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("anthropic-version", "2023-06-01")
		httpReq.Header.Set("accept", "text/event-stream")
		if isClaude {
			token, err := claudeAccessToken(ctx, c.HTTP, forceRefresh)
			if err != nil {
				return nil, err
			}
			httpReq.Header.Set("Authorization", "Bearer "+token)
			httpReq.Header.Set("anthropic-beta", claudeOAuthBeta)
		} else {
			httpReq.Header.Set("x-api-key", c.APIKey)
		}
		resp, err := c.HTTP.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("http do: %w", err)
		}
		return resp, nil
	}

	resp, err := send(false)
	if err == nil && isClaude && resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		resp, err = send(true)
	}
	if err != nil {
		return ChatMessage{}, Usage{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return ChatMessage{}, Usage{}, fmt.Errorf("anthropic status %d: %s", resp.StatusCode, truncateForLog(string(bodyBytes)))
	}

	return parseSSE(ctx, resp.Body, onEvent)
}

// parseSSE reads Anthropic-style SSE events (lines beginning with "event: "
// or "data: ") and assembles the assistant message. Anthropic sends
// content_block_delta events for text and thinking, and a final
// message_delta with stop_reason and usage.
//
// Emits a stream_event to onEvent for each raw chunk so the UI's debug
// drawer can show the wire format. The chunk shape is the parsed Anthropic
// event (StreamChunk).
func parseSSE(ctx context.Context, body io.Reader, onEvent func(map[string]any)) (ChatMessage, Usage, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		msg           ChatMessage
		usage         Usage
		currentEvent  string
		toolCallsByIx = map[int]*ToolCall{}
		// tracks which tool_use indices have received at least one input_json_delta
		// so we can discard the placeholder input:{} from content_block_start.
		toolCallDeltaSeen = map[int]bool{}
		finalStopReason   string
		// Block-level accumulators so we can build msg.Content (text only) and
		// the tool_use list, mirroring what buildMessages expects.
		assembledText     strings.Builder
		assembledThinking strings.Builder
	)

	flushData := func(line string) {
		if line == "" {
			return
		}
		var chunk StreamChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			log.Printf("[minimax] chunk parse: %v: %s", err, truncateForLog(line))
			return
		}
		onEvent(map[string]any{
			"type":  "stream_event",
			"event": chunk,
		})

		switch chunk.Type {
		case "message_start":
			if chunk.Message != nil {
				if msg.Role == "" {
					msg.Role = "assistant"
				}
				if chunk.Message.Usage != nil {
					usage.PromptTokens = chunk.Message.Usage.PromptTokens
					usage.CacheRead = chunk.Message.Usage.CacheRead
					usage.CacheWrite = chunk.Message.Usage.CacheWrite
				}
			}

		case "content_block_start":
			if chunk.ContentBlock == nil {
				return
			}
			idx := chunk.Index
			log.Printf("[minimax] content_block_start idx=%d type=%q id=%q name=%q input_len=%d",
				idx, chunk.ContentBlock.Type, chunk.ContentBlock.ID, chunk.ContentBlock.Name, len(chunk.ContentBlock.Input))
			if chunk.ContentBlock.Type == "tool_use" {
				args := ""
				if len(chunk.ContentBlock.Input) > 0 && string(chunk.ContentBlock.Input) != "null" {
					// Some proxies include the full input in content_block_start
					// instead of streaming it via input_json_delta events.
					args = string(chunk.ContentBlock.Input)
				}
				toolCallsByIx[idx] = &ToolCall{
					Type: "function",
					ID:   chunk.ContentBlock.ID,
					Function: ToolCallFunc{
						Name:      chunk.ContentBlock.Name,
						Arguments: args,
					},
				}
			}

		case "content_block_delta":
			if chunk.Delta == nil {
				return
			}
			idx := chunk.Index
			switch chunk.Delta.Type {
			case "text_delta":
				assembledText.WriteString(chunk.Delta.Text)
			case "thinking_delta":
				assembledThinking.WriteString(chunk.Delta.Thinking)
			case "input_json_delta":
				if tc, ok := toolCallsByIx[idx]; ok {
					if !toolCallDeltaSeen[idx] {
						// First delta for this block: discard any placeholder
						// input:{} that content_block_start may have included.
						// The real input is streamed entirely via deltas.
						tc.Function.Arguments = ""
						toolCallDeltaSeen[idx] = true
					}
					tc.Function.Arguments += chunk.Delta.PartialJSON
				}
			}

		case "content_block_stop":
			// No-op: block boundaries are already accounted for in deltas.
			_ = chunk.Index

		case "message_delta":
			if chunk.Delta != nil && chunk.Delta.StopReason != "" {
				finalStopReason = chunk.Delta.StopReason
			}
			if chunk.Usage != nil {
				usage.CompletionTokens = chunk.Usage.CompletionTokens
				usage.TotalTokens = usage.PromptTokens + chunk.Usage.CompletionTokens
			}

		case "message_stop":
			// Done.
		}
	}

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return msg, usage, err
		}
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			currentEvent = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			payload := strings.TrimPrefix(line, "data: ")
			flushData(payload)
			_ = currentEvent
		}
	}
	if err := scanner.Err(); err != nil {
		return msg, usage, fmt.Errorf("scan sse: %w", err)
	}

	// Finalize the assistant message. We merge the thinking block back
	// into Content using the same <think>...</think> delimiter that
	// splitThinking() already parses, so the Run() loop can extract
	// finalThinking and the next round's history replay carries the
	// model's reasoning verbatim.
	plainText := strings.TrimSpace(assembledText.String())
	plainThinking := strings.TrimSpace(assembledThinking.String())
	if plainThinking != "" {
		plainText = "<think>" + plainThinking + "</think>\n\n" + plainText
	}
	msg.Role = "assistant"
	msg.Content = JSONQuote(plainText)
	if len(toolCallsByIx) > 0 {
		// Collect and sort indices so tool calls are appended in stream order.
		// Do NOT iterate 0..len-1: the thinking block occupies index 0, so
		// tool_use blocks arrive at index 1+ and would be silently dropped
		// by a sequential loop that stops at len(map)-1.
		idxs := make([]int, 0, len(toolCallsByIx))
		for i := range toolCallsByIx {
			idxs = append(idxs, i)
		}
		sort.Ints(idxs)
		msg.ToolCalls = make([]ToolCall, 0, len(idxs))
		for _, i := range idxs {
			msg.ToolCalls = append(msg.ToolCalls, *toolCallsByIx[i])
		}
	}
	if finalStopReason != "" {
		// "end_turn" → "stop", "tool_use" → empty (loop continues), "max_tokens" → "length"
		switch finalStopReason {
		case "end_turn":
			msg.FinishReason = "stop"
		case "max_tokens":
			msg.FinishReason = "length"
		case "tool_use":
			msg.FinishReason = "tool_calls"
		default:
			msg.FinishReason = finalStopReason
		}
	}
	// Store the accumulated thinking in usage.ReasoningTokens as a side
	// channel — the Run() loop reads it via a separate path. (We attach
	// it to the last text-block on the call site; see Run splitThinking.)
	// For now, surface it through the usage block to keep things simple.
	_ = assembledThinking
	return msg, usage, nil
}

// thinkRE matches <think>...</think> blocks (MiniMax streams thinking inline).
var thinkRE = regexp.MustCompile(`(?s)<think>(.*?)</think>`)

// splitThinking returns (visibleText, thinkingText) for a chunk of model
// output. Multiple <think> blocks are concatenated with newlines.
func splitThinking(content string) (text, thinking string) {
	matches := thinkRE.FindAllStringSubmatch(content, -1)
	text = thinkRE.ReplaceAllString(content, "")
	text = strings.TrimSpace(text)
	var parts []string
	for _, m := range matches {
		parts = append(parts, m[1])
	}
	thinking = strings.TrimSpace(strings.Join(parts, "\n"))
	return
}

// buildMessages builds the initial messages array for a turn: a system
// message describing the tool runtime, the replayed history from the DB, and
// the current user message. The message parameter is the processed user text
// (which may have been enriched with an image description by understandImage).
func buildMessages(req TurnRequest, message string) []ChatMessage {
	var msgs []ChatMessage
	msgs = append(msgs, ChatMessage{
		Role:    "system",
		Content: JSONQuote(systemPrompt),
	})
	workDir := ""
	if len(req.WorkDirs) > 0 {
		workDir = req.WorkDirs[0]
	}
	if ctx := buildUserContextBlock(workDir, req.Model); ctx != "" {
		msgs = append(msgs, ChatMessage{
			Role:    "system",
			Content: JSONQuote(ctx),
		})
	}
	if strings.TrimSpace(req.SystemPrompt) != "" {
		msgs = append(msgs, ChatMessage{
			Role:    "system",
			Content: JSONQuote("# Agent instructions\n" + req.SystemPrompt),
		})
	}
	for _, h := range req.History {
		msgs = append(msgs, ChatMessage{
			Role:    h.Role,
			Content: JSONQuote(h.Content),
		})
	}
	// Note: ImageBase64 is no longer sent as a content part — it was pre-processed
	// by understandImage() in Run() and the result is already in the `message` arg.
	msgs = append(msgs, ChatMessage{Role: "user", Content: JSONQuote(message)})
	return msgs
}

// buildUserContextBlock returns a system prompt block with stable environment
// context: working directory, platform, and model. Git status is computed by
// the orchestrator (which runs locally) and passed via TurnRequest.UserContext.
func buildUserContextBlock(workDir, model string) string {
	var sb strings.Builder
	sb.WriteString("# Environment\n")
	if workDir != "" {
		sb.WriteString(" - Primary working directory: " + workDir + "\n")
	}
	sb.WriteString(" - Platform: " + runtime.GOOS + "\n")
	if model != "" {
		sb.WriteString(" - Model: " + model + "\n")
	}
	return sb.String()
}

// runToolWithPermission is the single chokepoint where aiapi asks the
// orchestrator for permission before executing a tool. The orchestrator
// decides via its PermissionStore (settings.json hierarchy + session
// rules) or by prompting the user. On allow we run the tool; on deny
// or any error we synthesise a tool_result so the model can adapt.
func runToolWithPermission(
	ctx context.Context,
	perm *PermissionClient,
	tc ToolCall,
	workDir string,
	wm *worktree.Manager,
	req TurnRequest,
	onEvent func(map[string]any),
) string {
	argsRaw := json.RawMessage(tc.Function.Arguments)
	decision, reason, err := perm.Request(ctx, tc.ID, tc.Function.Name, argsRaw, workDir)
	if err != nil || (decision != "allow" && decision != "deny") {
		// Fail closed: treat any error / unknown decision as deny.
		if err == nil {
			err = fmt.Errorf("orchestrator returned decision=%q", decision)
		}
		reason = "approval backend error: " + err.Error()
		decision = "deny"
	}

	if decision == "allow" {
		if isTaskTool(tc.Function.Name) {
			return callTaskTool(ctx, perm, tc.Function.Name, argsRaw)
		}
		return executeTool(tc.Function.Name, argsRaw, workDir, wm)
	}

	// deny: synthesise a tool_result, publish a tool_pending event so
	// the UI can show the denial, and return the reason as the
	// tool_result content. The agentic loop continues.
	onEvent(map[string]any{
		"type":        "tool_pending",
		"session_id":  req.SessionID,
		"tool_use_id": tc.ID,
		"tool_name":   tc.Function.Name,
		"tool_input":  argsRaw,
		"decision":    "deny",
		"reason":      reason,
	})
	return fmt.Sprintf("Tool execution was denied: %s", reason)
}

func truncateForLog(s string) string {
	const max = 500
	if len(s) <= max {
		return s
	}
	return s[:max] + "...(truncated)"
}
