# Frontend

## Stack

- **React 18** + TypeScript + Vite (dev `:5173`, build to `web/dist/`)
- **react-router-dom** ⭐ — client-side routing, URL-based navigation
- **Zustand** — global state store (`web/src/store/index.ts`)
- **Framer Motion** — message stagger animations, modal transitions
- **Native WebSocket** — one connection per active session

## Router — `web/src/routes.tsx` ⭐

URL-based navigation for persistent page state on reload:

```
/                                    — landing (no project/session selected)
/projects/:projectId                 — project selected, no session
/projects/:projectId/sessions/:sessionId  — project + session selected
```

**Sync mechanism** (`App.tsx`):
- `useParams()` reads `:projectId` and `:sessionId` from URL
- Effects sync URL params → Zustand store (`setActiveProject`, `setActiveSession`)
- NavPanel and App both call `useNavigate()` to push URL changes
- On page reload: URL params restore the last viewed project/session

**No routing logic in components** — App.tsx is always rendered. Route params only set Zustand state; layout never changes based on route.

## Layout

3-panel layout:
```
┌──────────────┬───────────────┬──────────────────────────────┐
│  Projects    │  Sessions     │  Chat                        │
│  (180px)     │  (240px)      │  (flex-1)                    │
│  [+ New]     │  [+]  ▲       │  messages list (virtual)     │
│              │  (edit mode)  │  user image bubbles + zoom   │
│  project 1 ● │  session 1    │  streaming thinking/text     │
│  project 2   │  session 2 ●  │  input: +img brain effort    │
└──────────────┴───────────────┴──────────────────────────────┘
  HookToastContainer floats top-right with countdown
```

## Component Tree

```
main.tsx
└── BrowserRouter ⭐
    └── AppRoutes ⭐ (web/src/routes.tsx)
        └── App.tsx (all routes render this)
            ├── Sidebar/NavPanel.tsx      — projects tree + sessions (collapsed view)
            ├── Toast/HookToastContainer.tsx — renders all pending hook toasts (top-right) ⭐
            └── Chat/ChatView.tsx            — main chat panel
                ├── Chat/MessageBubble.tsx   — renders message: user (text+images) / assistant / 
                │                              thinking / hook (decision badge) / raw
                ├── Chat/InputControls.tsx   — effort + thinking toggle + model selector ⭐
                │   └── Chat/AttachmentButton.tsx — file picker for images ⭐
                ├── Chat/ThinkingIndicator.tsx — animated dots while Claude is working
                └── Image preview + send button
```

⭐ = Recent additions

## REST API — `web/src/api.ts`

Base URL: `http://localhost:8090`

```typescript
listProjects()
createProject(data)
deleteProject(id)

listSessions(pid)
createSession(pid, { message?, name?, model?, effort?, thinking?, image? })  ⭐
deleteSession(id)
renameSession(id, name)  ⭐
sendTurn(id, message, model?, effort?, thinking?, image?)  ⭐
forkSession(id, name, message?)

listMessages(id, opts?: { limit?: number; beforeSeq?: number })
  → { messages: Message[], has_more: boolean, oldest_seq: number }
```

**Image format** (optional in createSession + sendTurn):
```typescript
image?: { base64: string; mediaType: string }
```

Supports cursor-based pagination: `beforeSeq` parameter for lazy-loading older messages.
Supports effort levels: `auto|low|medium|high|max`
Supports thinking modes: `enabled|disabled|adaptive`

## Zustand Store — `web/src/store/index.ts`

### State shape

```typescript
{
  projects: Project[]
  sessions: Session[]
  activeProjectId: string | null
  activeSessionId: string | null
  pendingSession: { projectId, model } | null  ⭐  // lazy session creation
  messages: Record<string, Message[]>
  messagePagination: Record<string, { hasMore, oldestSeq, isLoadingMore }>  // per session
  pendingHooks: Record<string, PendingHook[]>   // queue per session + created_at ⭐
  sessionStatus: Record<string, string>
  streamingText: Record<string, string>          // flat strings, reliable equality
  streamingThinking: Record<string, string>      // thinking block in progress
  sessionUsage: Record<string, TokenUsage>       // accumulated per session
}
```

**pendingSession** ⭐: When user clicks "+" in sessions, state goes to pending until first message. Allows picking model before naming session. On first sendMessage, creates session + inserts message in single request.

**pendingHooks** ⭐: Now includes `created_at` for countdown timer in toast (4-minute timeout).

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

### Hook queue management ⭐

```typescript
// hook_pending event → push to pendingHooks[session_id] + create virtual "hook" message in chat
// hook_resolved event → filter from queue + update virtual message with decision
// HookToastContainer renders all pending hooks as toasts (top-right, one per hook)
// Each toast shows 4-minute countdown based on created_at
```

**Message flow for hooks**:
1. `hook_pending` arrives → virtual message added with `role: "hook"`, `decision: "pending"` (JSON)
2. User clicks Approve/Deny in toast → `sendHookRespond(toolUseId, decision)`
3. `hook_resolved` broadcast → virtual message updated with `decision: "approve|deny|timeout"`
4. On page reload, hook messages load from DB (persisted with their decisions)

## WebSocket Hook — `web/src/hooks/useSessionWS.ts`

Opens `ws://localhost:8090/ws?role=client&session_id=<id>` when a session is active.

- Reconnects with exponential backoff (1s → 2s → 4s … 30s max)
- On reconnect, backend re-sends pending hooks (if any) so modal reappears
- `sendHookRespond(toolUseId, decision, reason?)` — sends `hook_respond` with `tool_use_id`

## App.tsx — `web/src/App.tsx` ⭐

Main component that handles router integration and state sync:

```typescript
const { projectId, sessionId } = useParams()  // Read URL params
const navigate = useNavigate()

// On mount: load projects
useEffect(() => loadProjects(), [loadProjects])

// URL → Store: when URL params change, update active project/session
useEffect(() => {
  if (projectId) setActiveProject(projectId)
}, [projectId, setActiveProject])

useEffect(() => {
  if (sessionId) setActiveSession(sessionId)
}, [sessionId, setActiveSession])

// Store → URL: when store selection changes, push new URL
useEffect(() => {
  if (activeProjectId && activeSessionId) {
    navigate(`/projects/${activeProjectId}/sessions/${activeSessionId}`, { replace: true })
  } else if (activeProjectId) {
    navigate(`/projects/${activeProjectId}`, { replace: true })
  }
}, [activeProjectId, activeSessionId, navigate])
```

**Key**: two-way sync ensures URL and store stay in sync, so page refresh restores state.

## ChatView — `web/src/components/Chat/ChatView.tsx`

### Pending session flow ⭐

1. SessionList "+ " button calls `setPendingSession(projectId, model)`
2. ChatView shows empty state with placeholder message input  
3. User types message + optionally attaches image
4. On Send: `sendMessage` calls `api.createSession(projectId, { message, image, name: auto-from-message })`
5. Response creates session, inserts message, broadcasts status
6. ChatView auto-switches to new session

### Image upload ⭐

1. **File picker**: `AttachmentButton` (paperclip icon, left of thinking toggle)
   - Accepts JPEG, PNG, GIF, WebP
   - Shows preview (max 120px) with remove button
   
2. **Paste**: Direct paste into textarea
   - Detects image in clipboard, extracts file, converts to base64
   - Shows preview above input area

3. **Message storage**: If image attached, user message stored as JSON content blocks:
   ```json
   [
     { "type": "text", "text": "message text" },
     { "type": "image", "source": { "type": "base64", "media_type": "image/png", "data": "..." } }
   ]
   ```

### Virtual scrolling

Uses `@tanstack/react-virtual` to render only visible messages + 8-item overscan. Measures dynamic heights (thinking blocks, code blocks, images, etc.) with `measureElement`. On page load, fetches latest 50 messages; scrolling up to 200px from top triggers `loadMoreMessages()` for pagination.

Scroll position is preserved via `useLayoutEffect` when prepending older messages — saves previous `scrollHeight`, then restores `scrollTop` after state update.

### Message ordering ⭐

Messages are **sorted by `created_at`** timestamp in ascending order:
- `loadMessages()` sorts after loading initial messages
- `loadMoreMessages()` sorts combined (older + existing) before storing
- New messages always arrive with correct timestamp, maintaining sort order

This ensures correct chronological display even when loading pages out of order.

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

## InputControls ⭐ — `web/src/components/Chat/InputControls.tsx`

Row of controls below textarea:

**Left side** (image + thinking):
- **AttachmentButton** (paperclip +): file picker for images
- **Thinking toggle** (brain icon): `enabled | disabled` (adaptive is server-side default)
  - Toggles `thinking: "enabled" | "disabled"`
  - Icon changes color when active (accent green)
  - Tooltip on hover

**Center/Right** (effort + model + send):
- **Effort selector** (Headless UI Listbox)
  - Options: `auto | low | medium | high | max`
  - Sent to CLI as `--effort=<value>` (if not "auto")
- **Model selector** (Headless UI Listbox)
  - Options: Haiku 4.5, Sonnet 4.6, Opus 4.6
  - Overrides project default per-message
- **Send button**: `↑` (up arrow), disabled when input empty or session running

All controls disabled when `isRunning=true`.

## HookToastContainer + HookToast ⭐ — `web/src/components/Toast/`

**HookToastContainer**: Fixed top-right overlay, renders one toast per pending hook from **inactive sessions only**.

**HookToast** (per hook):
- Shows tool name (e.g. "⚙ Tool Use — Bash")
- Tool input preview (via shared `ToolInputRenderer`)
- **Countdown progress bar**: 4-minute timeout, updates every second
- **Clickable**: Redirects to the session on click (using `useNavigate`)
- **No action buttons**: Approval/Denial only in chat (see MessageBubble)
- **Elegant styling**: Semi-transparent backdrop, dark theme consistent

**Active session behavior**: If a hook is from the active session, no toast is shown; instead, approval buttons appear in the chat message itself.

**Shared tool renderers** ⭐ — `web/src/components/Hook/HookToolInput.tsx`:
- **Bash** — displays `$ command` in code block + `description` as caption
- **Write** — shows filepath (dir/filename) + file content in code block
- **Edit** — shows filepath + diff view (old_string with `−` red, new_string with `+` teal)
- **Read/Glob/Grep/LS** — clean filepath or pattern display
- **Fallback** — raw JSON for unknown tools

Used by both HookToast and hook Message role in chat.

## NavPanel — `web/src/components/Sidebar/NavPanel.tsx` ⭐

Merged Projects + Sessions into single collapsible tree. Uses `useNavigate()` to sync URL:

```typescript
const navigate = useNavigate()

const handleSelectProject = async (id: string) => {
  await setActiveProject(id)
  navigate(`/projects/${id}`)  // ⭐ Push URL when selecting
}

// Session click
onClick={() => {
  setActiveSession(s.id)
  navigate(`/projects/${activeProjectId}/sessions/${s.id}`)  // ⭐ Full path
}}
```

All project/session selections trigger `navigate()` to update the URL. Combined with App.tsx's URL → Store sync, this ensures consistent state.

## MessageBubble — `web/src/components/Chat/MessageBubble.tsx`

Each message shows a timestamp (HH:mm today, MMM D · HH:mm this year, MMM D YYYY · HH:mm other years).

Markdown rendering via `marked` library for assistant messages (headings, lists, code blocks, blockquotes, tables, links).

| `message.role` | Rendered as |
|---|---|
| `user` | Text + inline images (JSON content blocks) ⭐ |
| `assistant` | Markdown-rendered bubble + token badge + timestamp |
| `thinking` | Collapsible `<details>` with "◈ Thinking" summary + timestamp |
| `hook` | Tool use with decision badge (pending/✓ approved/✗ denied/⏱ timeout) ⭐ |
| `system` | Preformatted (error/info messages) + timestamp |
| `raw` | Collapsible `<details>` with raw JSON + timestamp |

**User messages with images** ⭐:
- Parsed from JSON content blocks when stored as such
- Text and images render inline in bubble
- Images wrapped in `<Zoom>` from `react-medium-image-zoom`
- Click image → full-screen zoom with elegant backdrop blur (10px) + pan/zoom controls

**Hook messages** ⭐:
- Shows tool name (e.g. "⚙ Tool Use — Bash")
- Displays tool input (command, file path, etc.) via `ToolInputRenderer`
- Decision badge with color: pending ⏳, approved ✓ (green), denied ✕ (red), timeout ⏱ (orange)
- **If pending**: Shows [Deny] and [Approve] buttons to make decision directly in chat
- Also displays as non-interactive toast in HookToastContainer if from inactive session

Token badge shows: `↑input ↓output cache↩read` (tokens used, cache hit stats).

## Types — `web/src/types.ts`

```typescript
interface PendingHook {
  session_id: string
  tool_use_id: string   // required for routing
  tool_name: string
  tool_input: unknown
  created_at: string    // ⭐ ISO 8601, for countdown timer
}

interface Message {
  id: string
  session_id: string
  role: 'user' | 'assistant' | 'thinking' | 'hook' | 'system' | 'raw'  // ⭐ added 'hook'
  content: string  // plain text OR JSON (user with images, hook with decision)
  seq: number
  created_at: string
  usage?: TokenUsage
}

type WSEvent =
  | { type: 'session_status'; session_id: string; status: string }
  | { type: 'stream_event'; session_id: string; event: unknown; partial?: boolean }
  | { type: 'hook_pending'; session_id: string; tool_use_id: string; tool_name: string; tool_input: unknown; created_at: string }  // ⭐
  | { type: 'hook_resolved'; session_id: string; tool_use_id: string; decision: 'approve' | 'deny' | 'timeout' }  // ⭐
  | { type: 'turn_complete'; session_id: string; result_subtype: string; result_text: string }
  | { type: 'error'; session_id: string; message: string }
  | { type: 'info'; session_id: string; message: string }
```
