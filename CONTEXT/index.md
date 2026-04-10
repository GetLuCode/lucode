# Project Context Index

Claude Code Session Orchestrator — a web UI that wraps the `claude` CLI as an API, supporting multiple concurrent sessions, real-time streaming, and interactive tool approval via WebSocket.

## Context Files

| File | Covers | Key source files |
|------|--------|-----------------|
| [backend.md](backend.md) | Go server architecture, HTTP API, session manager, CLI process lifecycle, image stdin format | `cmd/lucode/main.go`, `internal/api/`, `internal/session/` |
| [websocket.md](websocket.md) | WS hub, message routing, role system (client/hook), all event types | `internal/ws/hub.go`, `internal/api/handler.go` |
| [hook.md](hook.md) | Hook binary, PreToolUse approval flow, concurrent tool routing by tool_use_id, timeout handling | `cmd/lucodehook/main.go`, `internal/session/manager.go` |
| [frontend.md](frontend.md) | React app, Zustand store, streaming text/thinking, hook toast notifications, lazy sessions, image upload/display, effort/thinking controls | `web/src/` |
| [database.md](database.md) | SQLite schema (including JSON content blocks for images), all tables, migration strategy | `internal/db/` |
| [command-engine.md](command-engine.md) | Voice command detection engine: hotword → LLM classification → tool execution pipeline, Goja sandbox, user tool registry | `internal/toolengine/`, `internal/session/command.go`, `web/src/hooks/useCommandDetection.ts`, `web/src/lib/toolExecutor.ts`, `web/src/components/Settings/ToolsTab.tsx` |
| [issues.md](issues.md) | **Recurring issues, gotchas, patterns, and implementation checklists** — read before refactoring | Various |

## System Overview

```
Browser UI (React + Zustand)
  │  REST: http://localhost:8090/api/...
  │  WS:   ws://localhost:8090/ws?role=client&session_id=<id>
  ▼
Go Backend (port 8090)
  ├─ Hub: session-keyed WS clients, two roles (client / hook)
  ├─ Manager: one goroutine per active claude turn
  ├─ ToolEngine: Goja VM sandbox for server-side user tool execution
  └─ SQLite: ~/.claude-orchestrator/orchestrator.db
  │
  ├── exec: claude CLI process (one per turn)
  │         stdout → stream-json lines → broadcast to WS
  │
  ├── Hook WS: ws://localhost:8090/ws?role=hook&session_id=<id>
  │            ↑ connected by hook binary on each PreToolUse
  │
  └── Command Detection: POST /api/command-detect
               ↑ voice transcript → Anthropic API (Haiku) → tool_call or null
```

## Critical Invariants

1. **Port 8090** — hardcoded in `web/src/api.ts` and `web/src/hooks/useSessionWS.ts`.
2. **Goroutine per turn** — `StartTurn` is non-blocking; `streamOutput` runs in its own goroutine.
3. **Hook routing by tool_use_id** — parallel tool calls each get their own WS connection; `SendToHookClient` routes by `tool_use_id`, NOT by broadcast.
4. **Zustand stable refs** — empty fallbacks must be module-level constants, never inline `?? []`.
5. **pending_hooks is ephemeral** — dropped and recreated on every server start; never store important state there.
