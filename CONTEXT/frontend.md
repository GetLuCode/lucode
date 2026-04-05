# Frontend

## Stack

- **React 18** + TypeScript + Vite (dev `:5173`, build to `web/dist/`)
- **Zustand** — global state store (`web/src/store/index.ts`)
- **Framer Motion** — message stagger animations, modal transitions
- **Native WebSocket** — one connection per active session

## Layout

3-panel layout:
```
┌──────────────┬───────────────┬──────────────────────────────┐
│  Projects    │  Sessions     │  Chat                        │
│  (180px)     │  (240px)      │  (flex-1)                    │
│  [+ New]     │  [+ New]      │  messages list               │
│              │               │  streaming thinking bubble   │
│  project 1 ● │  session 1    │  streaming text bubble       │
│  project 2   │  session 2 ●  │  ToolApprovalModal (overlay) │
└──────────────┴───────────────┴──────────────────────────────┘
```

## Component Tree

```
App.tsx
├── Sidebar/ProjectList.tsx    — list + create/delete projects
├── Sidebar/SessionList.tsx    — list + create/delete/fork sessions
└── Chat/ChatView.tsx          — main chat panel
    ├── Chat/MessageBubble.tsx — renders one message (user/assistant/thinking/raw/system)
    ├── Chat/ThinkingIndicator.tsx — animated dots while Claude is working
    └── Chat/ToolApprovalModal.tsx — frosted-glass modal for hook approval
```

## REST API — `web/src/api.ts`

Base URL: `http://localhost:8090`

```typescript
listProjects()
createProject(data)
deleteProject(id)

listSessions(pid)
createSession(pid, { message?, name?, model? })
deleteSession(id)
sendTurn(id, message, model?)
forkSession(id, name, message?)

listMessages(id, opts?: { limit?: number; beforeSeq?: number })
  → { messages: Message[], has_more: boolean, oldest_seq: number }
```

Supports cursor-based pagination: `beforeSeq` parameter for lazy-loading older messages.

## Zustand Store — `web/src/store/index.ts`

### State shape

```typescript
{
  projects: Project[]
  sessions: Session[]
  activeProjectId: string | null
  activeSessionId: string | null
  messages: Record<string, Message[]>
  messagePagination: Record<string, { hasMore, oldestSeq, isLoadingMore }>  // per session
  pendingHooks: Record<string, PendingHook[]>   // queue per session
  sessionStatus: Record<string, string>
  streamingText: Record<string, string>          // flat strings, reliable equality
  streamingThinking: Record<string, string>      // thinking block in progress
  sessionUsage: Record<string, TokenUsage>       // accumulated per session
}
```

### Mutable state outside Zustand (no re-render needed)

```typescript
// Token usage accumulator during a turn
const pendingUsageMap: Record<string, PendingUsage> = {}

// Maps content block index to its type ('text' | 'thinking') per session
const blockTypeMap: Record<string, Record<number, string>> = {}
```

`blockTypeMap` is needed because `content_block_delta` events only include the block index, not the type. The type is captured from `content_block_start`.

### Legacy message conversion

`replayStoredMessages(raw: Message[])` converts old-format stored messages (where `content = '{"type":"stream_event",...}'`) to clean text messages. It replays through the same state-machine as live `handleWSEvent`, so messages loaded from REST API match the format of WS-streamed messages. New messages from the backend are already clean and pass through unchanged.

### Streaming text pipeline

```
message_start
  → pendingUsageMap[sessId] = { inputUsage }
  → blockTypeMap[sessId] = {}
  → clear streamingText[sessId] and streamingThinking[sessId]

content_block_start (index N, type "text"|"thinking")
  → blockTypeMap[sessId][N] = type

content_block_delta (type "text_delta")
  → streamingText[sessId] += chunk

content_block_delta (type "thinking_delta")
  → streamingThinking[sessId] += chunk

content_block_stop (index N)
  → if blockTypeMap[sessId][N] == "thinking":
      finalize thinking block → add Message{role: "thinking"} to messages list
      clear streamingThinking[sessId]

message_delta
  → pendingUsageMap[sessId].outputTokens = usage.output_tokens

message_stop
  → finalize text: add Message{role: "assistant", content, usage} to messages
  → accumulate sessionUsage[sessId]
  → clear streamingText, streamingThinking
  → delete pendingUsageMap[sessId], blockTypeMap[sessId]
```

### Hook queue management

```typescript
// hook_pending event → push to pendingHooks[session_id] queue (deduplicate by tool_use_id)
// hook_resolved event → filter out matching tool_use_id from queue
// ChatView shows pendingHooks[0] (first in queue) in the modal
```

## WebSocket Hook — `web/src/hooks/useSessionWS.ts`

Opens `ws://localhost:8090/ws?role=client&session_id=<id>` when a session is active.

- Reconnects with exponential backoff (1s → 2s → 4s … 30s max)
- On reconnect, backend re-sends pending hooks (if any) so modal reappears
- `sendHookRespond(toolUseId, decision, reason?)` — sends `hook_respond` with `tool_use_id`

## ChatView — `web/src/components/Chat/ChatView.tsx`

### Virtual scrolling

Uses `@tanstack/react-virtual` to render only visible messages + 8-item overscan. Measures dynamic heights (thinking blocks, code blocks, etc.) with `measureElement`. On page load, fetches latest 50 messages; scrolling up to 200px from top triggers `loadMoreMessages()` for pagination.

Scroll position is preserved via `useLayoutEffect` when prepending older messages — saves previous `scrollHeight`, then restores `scrollTop` after state update.

### Key selectors (all use stable empty refs to avoid infinite loops)

```typescript
const EMPTY_MESSAGES: Message[] = []
const EMPTY_HOOKS: PendingHook[] = []

const messages = useStore(s =>
  activeSessionId ? (s.messages[activeSessionId] ?? EMPTY_MESSAGES) : EMPTY_MESSAGES
)
const pendingHooks = useStore(s =>
  activeSessionId ? (s.pendingHooks[activeSessionId] ?? EMPTY_HOOKS) : EMPTY_HOOKS
)
```

**CRITICAL**: Never use `?? []` inline in selectors — each call creates a new array, breaking `Object.is` comparison and causing an infinite re-render loop.

### Streaming display order

```
[past messages]
[streaming thinking bubble — live, open <details>]
[streaming text bubble — live, with blinking cursor]
[ThinkingIndicator — 3 dots, only if no streaming content]
```

### Model selector

```typescript
const MODELS = [
  { label: 'Haiku 4.5',  value: 'haiku' },
  { label: 'Sonnet 4.6', value: 'sonnet' },
  { label: 'Opus 4.6',   value: 'opus' },
]
```

Model value is sent as-is to `POST /api/sessions/{id}/turn`. The backend maps it to the CLI `--model=` arg.

## ToolApprovalModal — `web/src/components/Chat/ToolApprovalModal.tsx`

Receives `hook` (first in queue) and `queueLength`. Shows "N pending" when multiple hooks are queued. Approve/Deny calls `sendHookRespond(hook.tool_use_id, decision)`.

Tool-specific renderers:
- **Bash** — displays `$ command` in code block + `description` as caption
- **Write** — shows filepath (dir/filename) + file content in code block
- **Edit** — shows filepath + diff view (old_string with `−` red, new_string with `+` teal)
- **Read/Glob/Grep/LS** — clean filepath or pattern display
- **Fallback** — raw JSON for unknown tools

## MessageBubble — `web/src/components/Chat/MessageBubble.tsx`

Each message shows a timestamp (HH:mm today, MMM D · HH:mm this year, MMM D YYYY · HH:mm other years).

Markdown rendering via `marked` library for assistant messages (headings, lists, code blocks, blockquotes, tables, links). User and system messages are plain text. Includes CSS styling for all markdown elements with the dark theme.

| `message.role` | Rendered as |
|---|---|
| `user` | Plain text bubble + timestamp |
| `assistant` | Markdown-rendered bubble + token badge + timestamp |
| `thinking` | Collapsible `<details>` with "◈ Thinking" summary + timestamp |
| `system` | Preformatted (used for error/info messages) + timestamp |
| `raw` | Collapsible `<details>` with raw JSON + timestamp |

Token badge shows: `↑input ↓output cache↩read` (tokens used, cache hit stats).

## Types — `web/src/types.ts`

```typescript
interface PendingHook {
  session_id: string
  tool_use_id: string   // required for routing
  tool_name: string
  tool_input: unknown
}

type WSEvent =
  | { type: 'session_status'; session_id: string; status: string }
  | { type: 'stream_event'; session_id: string; event: unknown; partial?: boolean }
  | { type: 'hook_pending'; session_id: string; tool_use_id: string; tool_name: string; tool_input: unknown }
  | { type: 'hook_resolved'; session_id: string; tool_use_id: string; decision: string }
  | { type: 'turn_complete'; session_id: string; result_subtype: string; result_text: string }
  | { type: 'error'; session_id: string; message: string }
  | { type: 'info'; session_id: string; message: string }
```
