# Claude Code Instructions

## Context Files

This project has structured documentation in `CONTEXT/`. Always read the relevant files before making changes.

**Start here:** `CONTEXT/index.md` — lists all context files and what each covers.

When working on any part of the system, read the relevant context file(s) before editing code:

- Backend Go code → `CONTEXT/backend.md`
- WebSocket protocol, hub routing → `CONTEXT/websocket.md`
- Hook binary, PreToolUse approval flow → `CONTEXT/hook.md`
- React frontend, Zustand store, streaming → `CONTEXT/frontend.md`
- Database schema, queries → `CONTEXT/database.md`

If unsure which file applies, read `CONTEXT/index.md` first — it has a one-line summary of each file and which source files each covers.

## Build & Run

```bash
# Backend
go build -o orchestrator ./cmd/orchestrator
./orchestrator                    # port 8090, data in ~/.claude-orchestrator/

# Hook binary (must be rebuilt + copied on every change)
go build -o hook ./cmd/hook
cp hook ~/.claude/hooks/orchestrator-hook

# Frontend
cd web && npm run dev             # dev server on :5173
cd web && npm run build           # production build to web/dist/
```

## Key Constraints

- After any Go change: rebuild `orchestrator` and/or `hook` and restart the server.
- Hook binary is installed at `~/.claude/hooks/orchestrator-hook` — always `cp` after rebuild.
- DB schema changes: `pending_hooks` is dropped and recreated on every server start (ephemeral). Other tables use `CREATE TABLE IF NOT EXISTS` — add explicit migrations for schema changes.
- Port: backend runs on **8090**. Frontend API and WS both hardcode `localhost:8090`.
- Zustand selectors that return objects/arrays must use stable module-level references (e.g. `const EMPTY: T[] = []`) instead of `?? []` — otherwise `Object.is` comparison breaks and causes infinite re-renders.
