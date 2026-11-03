# Command Detection Engine

Voice command detection system: hotword-triggered LLM classification → tool execution pipeline.

## Architecture

```
Browser STT (Web Speech API)
  │  continuous, interimResults
  │
  ▼
useVoiceInput (onTranscript)
  │
  ▼
useCommandDetection (processTranscript)
  │
  ├── No hotword / no tools / no account → passthrough to chat input
  │
  ├── Transcript doesn't start with hotword → passthrough to chat input
  │
  └── Hotword detected
        │  strip hotword prefix → command text
        │
        ▼
    POST /api/command-detect
        │  Go backend → Anthropic API (Haiku 4.5)
        │  tool_choice: { type: "any" }
        │  __no_match escape tool
        │
        ├── __no_match → passthrough to chat input
        │
        └── tool_call { tool_name, tool_id, arguments }
              │
              ├── execution_env: "browser"
              │     toolExecutor.ts → new Function('args', code)
              │     "use strict", async wrapper
              │
              └── execution_env: "orchestrator"
                    POST /api/tools/{id}/execute
                    Go → Goja VM sandbox
                    SDK: fs, http, log, JSON
                    10s timeout + vm.Interrupt()
```

## Source Files

| Layer | File | Purpose |
|-------|------|---------|
| **Frontend** | `web/src/hooks/useCommandDetection.ts` | Hotword detection + LLM classification + tool execution orchestration |
| **Frontend** | `web/src/lib/toolExecutor.ts` | Browser-side execution engine + schema validation |
| **Frontend** | `web/src/lib/voicePrefs.ts` | `voice_hotword` preference in localStorage |
| **Frontend** | `web/src/components/Settings/ToolsTab.tsx` | Tool CRUD UI (create/edit/delete forms) |
| **Frontend** | `web/src/components/Chat/VoiceBar.tsx` | Hotword input in voice settings modal |
| **Frontend** | `web/src/components/Chat/ChatView.tsx` | Integration: routes voice → command pipeline, shows feedback |
| **Frontend** | `web/src/components/Chat/InputControls.tsx` | Mic beat animation + command processing glow |
| **Backend** | `internal/session/command.go` | `DetectCommand()` + `ExecuteUserTool()` on Manager |
| **Backend** | `internal/toolengine/engine.go` | Goja VM sandbox with injected SDK |
| **Backend** | `internal/api/handler.go` | REST endpoints: tool CRUD, execute, command-detect |
| **Backend** | `internal/db/db.go` | `user_tools` table migration |
| **Backend** | `internal/db/queries.go` | `UserTool` struct + CRUD queries |

## User-Defined Tools

Tools are stored in the `user_tools` SQLite table (see [database.md](database.md)).

Each tool has:
- **name** (unique) — identifier used by the LLM to select the tool
- **description** — natural language; the LLM uses this to match voice commands
- **input_schema** — JSON Schema defining expected arguments
- **code** — JavaScript source executed when invoked
- **execution_env** — `browser` or `orchestrator`

### Tool Examples

**UI action tool** (browser):
```
name: ui_accept
description: Accept or reject the current pending hook approval in the UI
input_schema: {
  "type": "object",
  "properties": {
    "action": { "type": "string", "enum": ["accept", "reject"] }
  },
  "required": ["action"]
}
code: return { action: args.action };
execution_env: browser
```

**File reader tool** (orchestrator):
```
name: read_log
description: Read the last lines of a log file
input_schema: {
  "type": "object",
  "properties": {
    "path": { "type": "string" },
    "lines": { "type": "integer" }
  },
  "required": ["path"]
}
code:
  var content = fs.readFile(args.path);
  var all = content.split("\n");
  var n = args.lines || 20;
  return all.slice(-n).join("\n");
execution_env: orchestrator
```

## LLM Command Detection — `DetectCommand()`

### API Call

```
POST https://api.anthropic.com/v1/messages
model: claude-haiku-4-5-20251001
max_tokens: 256
tool_choice: { type: "any" }   ← forces tool-only response (CRITICAL)
```

### System Prompt

> You are a voice command classifier. Analyze the user's spoken transcript and determine if it is a command that matches one of your available tools.
> Rules:
> - If the transcript clearly matches a tool's purpose, call that tool with the appropriate arguments.
> - If the transcript is ambiguous or doesn't match any tool, call the special "__no_match" tool.
> - Extract arguments precisely from the spoken words.
> - Be conservative: only match when intent is clear.

### Tools Sent

All user-defined tools from `user_tools` table + `__no_match` escape tool:
```json
{
  "name": "__no_match",
  "description": "Use this when the transcript does not match any available command.",
  "input_schema": { "type": "object", "properties": {} }
}
```

### Response Handling

1. Parse `content[]` blocks from response
2. Find first `type: "tool_use"` block
3. If `name == "__no_match"` → return `{ tool_call: null }`
4. Otherwise → return `{ tool_call: { tool_name, tool_id, arguments } }`

## Goja Sandbox — `internal/toolengine/engine.go`

### Injected SDK

| Module | Methods | Notes |
|--------|---------|-------|
| `fs` | `readFile(path)`, `writeFile(path, content)`, `readDir(path)`, `exists(path)` | Path traversal protection; scoped to workDir |
| `http` | `fetch(url, options?)` → `{status, statusText, body, json?}` | 10s HTTP client timeout |
| `log` | `info(...)`, `warn(...)`, `error(...)` | Writes to Go `log.Printf` |
| `JSON` | `parse(s)`, `stringify(v)` | Standard JSON helpers |
| `args` | (object) | Parsed tool arguments |

### Security Model

1. **Fresh VM per execution** — no cross-execution state leakage
2. **10-second timeout** — `time.AfterFunc` + `vm.Interrupt("execution timeout exceeded")`
3. **Path traversal** — `filepath.Clean` + `strings.HasPrefix(resolved, workDir)` on all fs operations
4. **No ambient APIs** — no `require()`, no `process`, no `__dirname`, no Node.js builtins
5. **Strict wrapping** — user code wrapped in `(function() { ... })()` IIFE

## Browser Execution — `web/src/lib/toolExecutor.ts`

### Schema Validation

`validateArgs(args, schemaStr)` performs basic checks before execution:
- Required fields present and non-null
- Type matches for top-level properties (`string`, `number`, `boolean`, `array`, `integer`)
- Returns error string or `null` on success

### Execution

```typescript
const fn = new Function('args', `
  "use strict";
  return (async function() {
    ${code}
  })();
`)
const result = await fn(args)
```

- `"use strict"` prevents sloppy-mode foot-guns
- Async wrapper allows `await` in user code
- Errors caught and returned as `{ success: false, error: message }`

## Frontend Integration

### ChatView State

```typescript
commandProcessing: boolean     // LLM is classifying the transcript
commandFeedback: string | null // "Executed: toolName" or "Command error: ..." (3s auto-clear)
hotword: string                // from voicePrefs
userTools: UserTool[]          // loaded from store on mount
```

### Mic Button Visual States (InputControls)

| CSS Class | When | Visual |
|-----------|------|--------|
| `thinking-toggle--listening` | Recording active | Red pulse animation |
| `thinking-toggle--beat` | Interim speech detected while recording | Scale 1→1.15 beat |
| `thinking-toggle--command` | Command being processed by LLM | Purple glow pulse |

### Command Feedback (above input)

| Class | When | Color |
|-------|------|-------|
| `chat__command-feedback--processing` | LLM classifying | Purple/accent |
| `chat__command-feedback--success` | Tool executed successfully | Green |
| `chat__command-feedback--error` | Tool or detection error | Red |

## Reliability Constraints

1. **Conservative matching** — `__no_match` escape tool + system prompt says "only match when intent is clear"
2. **Schema validation** — arguments validated against JSON Schema before execution (both browser + server)
3. **Safe failures**:
   - Tool not found → `onCommandError("Tool X not found")`
   - Invalid arguments → `{ success: false, error: "Missing required argument: X" }`
   - Execution error → `{ success: false, error: "execution error: ..." }`
   - Timeout → `{ success: false, error: "timeout: execution timeout exceeded" }`
4. **No double-processing** — `processingRef` prevents concurrent command detection
5. **Hotword required** — no hotword configured = all transcripts passthrough (no false positives)

## Browser control (Edge over CDP) — `internal/cdp`

One Microsoft Edge shared by the process (`cdp.Default`), driven over the DevTools protocol (gorilla/websocket, flattened sessions). Elements are located through the **accessibility tree** (`Accessibility.getFullAXTree`: role + accessible name) and referenced as `e<backendNodeId>`; CSS selectors and raw coordinates are the fallback.

| Layer | File | Purpose |
|-------|------|---------|
| Core | `internal/cdp/launch.go` | `Launch` (window or `--headless=new`, own profile `~/.lucode/edge-profile`, port 9222; attaches if the port already answers), `Close` |
| Core | `internal/cdp/pages.go`, `ax.go`, `actions.go` | tabs, navigate, list/find elements, click, mouse, type, key, text, screenshot, JS dialogs |
| MCP | `internal/taskmcp/browser.go`, `internal/api/browsertools.go` | `browser_*` tools for the orchestrator (and agents that list them in `lucode_tools`); NOT in `Names()`, NOT auto-approved |
| Goja | `internal/toolengine/cdp.go` | `cdp` object for user tools (`cdp.launch`, `cdp.find`, `cdp.click`...); code that mentions `cdp.` gets a 60 s limit instead of 10 s |

The browser runs on the machine of the lucode hub (tool calls are forwarded there), whatever runner the session uses. Tests (`internal/cdp/cdp_test.go`, `internal/toolengine/cdp_test.go`) launch a real headless Edge with a temp profile and skip when Edge is missing.

## Local model: Google AI Mode (`internal/aimode`)

Model id `local:aimode` ("Google AI Mode (Edge)"), picked in the orchestrator's settings or the chat's model menu. It runs no claude: `Executor: "aimode"` → `Manager.runWithAIMode` → `aimode.Default.Ask`. A separate Edge (`cdp.New("aimode", 9333)`, profile `~/.lucode/aimode-profile`, its own state file) opens google.com, clicks "Modo IA" / "AI Mode" (fallback `?udm=50`), sends a first message that tells Gemini how to answer (`DefaultPrimer`, or `LUCODE_AIMODE_PRIMER`), then types each chat message into the page and reads the answer from the last `[data-scope-id="turn"] [data-container-id="main-col"]`; it is finished when the text stops changing (the class names on that page are obfuscated, the data attributes are not). One tab per lucode session (a new orchestrator session or a deleted one closes it). The window is visible by default so a human check can be solved; `LUCODE_AIMODE_HEADLESS=1` hides it. No tools, streaming or images. The live test needs the network: `LUCODE_LIVE_AIMODE=1 go test ./internal/aimode -run Live -v`.
