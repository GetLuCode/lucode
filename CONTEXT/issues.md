# Recurring Issues & Patterns

## Critical Issues (Prevent Infinite Loops)

### ⚠️ Zustand Selector Stability
**Problem**: Inline array/object creation in selectors (`?? []` or `{...}` each render).
**Why**: `Object.is` comparison fails when reference changes, causes infinite re-renders.
**Solution**: Module-level stable constants.

```typescript
// ❌ WRONG
const items = useStore(s => s.items ?? [])  // new [] every render

// ✅ RIGHT
const EMPTY_ITEMS: Item[] = []
const items = useStore(s => s.items ?? EMPTY_ITEMS)
```

**Status**: Fixed 5399fec; verify all new selectors follow this pattern.

---

## Architectural Patterns

### JSON Content Blocks for User Messages with Images

**Pattern** (since 6969b71):
- Frontend builds JSON when image attached
- Backend stores same JSON in DB
- Frontend parses JSON when rendering

```json
// User message with image
[
  { "type": "text", "text": "Hello" },
  { "type": "image", "source": { "type": "base64", "media_type": "image/png", "data": "..." } }
]

// User message plain text
"Hello"
```

**Implication**: MessageBubble must parse both formats.

---

### Hook Messages as First-Class Citizens

**Pattern** (since 616b5a9):
- Hooks now appear as messages in chat (role="hook")
- Persisted to DB with their decision
- Still broadcast via WS for real-time toast updates
- Virtual messages in store until DB message loaded

**Content format**:
```json
{ "tool_name": "Bash", "tool_input": {...}, "decision": "approve|deny|timeout" }
```

**Rendering**:
- Chat: MessageBubble role="hook"
- Toast: HookToast (real-time, in-flight)
- DB reload: loads hook messages from ListMessages

---

## Repeated Implementation Steps

### When Adding A New Control/Flag

**Checklist**:
1. **Frontend**
   - Add state in ChatView / InputControls
   - Add param to api.sendTurn + api.createSession
   - Pass through store.sendMessage
2. **Backend**
   - handler.go: accept in request body struct
   - TurnOptions struct (types.go)
   - process.go: add to `buildArgs()` or stdin
3. **Test**
   - CLI args correct (--flag=value)
   - Stored in DB (if persistence needed)
   - Survives page reload

**Example**: effort level (5399fec) + thinking mode (same commit)

---

## Known Gotchas

### 1. Image Base64 Length

Frontend paste/file picker converts to base64 — strings can be large (5MB image → ~6.7MB base64). No validation on size, assume network can handle it. If issues arise, add max-size validation in AttachmentButton.

### 2. Pending Session Timing

When user creates pending session, no DB record exists until first message sent. If user navigates away, pending session is lost. Current UX is acceptable (just re-click "+").

### 3. Hook Timeout = 4 Minutes (Hard)

Hook binary (`cmd/hook/main.go` line 113) has read deadline of 4 min. Frontend toast also counts 4 min. If they drift, UX shows timeout before hook actually times out or vice-versa. Keep them in sync.

**Both places**:
- `cmd/hook/main.go`: `conn.SetReadDeadline(time.Now().Add(4 * time.Minute))`
- Frontend HookToast: `endTime = new Date(created_at).getTime() + 4*60*1000`

### 4. Content Serialization

User messages can be:
- Plain text string
- JSON array of content blocks (if image)
- Hook messages are JSON objects

MessageBubble must handle all three. Regex / try-parse-as-JSON is the pattern.

---

## Port Hardcoding ⚠️

Port 8090 is hardcoded in:
- `web/src/api.ts` — `const BASE = 'http://localhost:8090'`
- `web/src/hooks/useSessionWS.ts` — WebSocket URL construction

If port changes, update both.

---

## Component Lifecycle Issues

### Streaming & State Cleanup

When a turn completes:
1. `streamOutput` reads `message_stop` (final result)
2. Broadcasts to WS
3. Sets session status → `idle`
4. Deletes `procs[sessID]`
5. Frontend clears `streamingText[sessID]`

If frontend doesn't clear streaming state, partial output from previous turn may remain visible. Use `stream_event type=message_start` to reset.

---

## Test Coverage Gaps

1. **Image rendering** — no size limits, can cause layout issues
2. **Concurrent hook queue** — multiple parallel tools in sequence (should work, untested)
3. **Session fork with images** — fork copies messages; verify images serialize correctly
4. **Resume after timeout** — if hook times out mid-turn, can session resume? (likely broken)

---

## Performance Concerns

### Virtual Scrolling Edge Cases

- Dynamic height measurement can miss images initially loading
- Solution: images must have explicit max-width in CSS so measure is stable

### Large Messages

- No hard limit on message size
- Very large images (20MB+) may cause memory issues
- Browser paste handler loads entire file into memory before base64

---

## Styling Consistency

### Dark Theme Colors
```
--bg:           #0c0c0e
--bg-panel:     #111114
--accent:       #00d4aa
--error:        #ff5f5f
--warning:      #f59e0b
```

When adding UI:
- Use these vars
- Test hover states
- Ensure contrast (WCAG AA minimum)

### Repeated Issue: Overflow Hidden

Input area had `overflow: hidden` which clipped Listbox dropdowns. Watch for this pattern when styling containers with dropdowns.

---

## Summary of Latest Features (6 commits)

| Commit | Feature | Status |
|--------|---------|--------|
| 6969b71 | Image display w/ zoom | ✅ Complete |
| 92038cd | Input controls UI | ✅ Complete |
| d7f0d0b | Hook toast wiring | ✅ Complete |
| 21db8d0 | WS reconnect | ✅ Complete |
| 616b5a9 | Hook toasts + chat | ✅ Complete |
| 5399fec | Lazy sessions + effort + thinking | ✅ Complete |

All features integrated; docs updated (this file).
