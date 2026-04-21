# Session Report — 2026-04-20

## Done

### 1. WebSocket route fix
- Moved WS endpoint from `GET /` to `GET /ws` in `internal/api/handler.go`
- Updated frontend WS URL in `web/src/hooks/useWSConnection.ts`
- Fixes: `GET /` was returning 400 for browser requests because the WS handler intercepted all traffic

### 2. air live-reload for aiapi
- Created `/home/homebox/projects/aiapi/.air.toml`
- `cmd = "go build -o ./tmp/server ./cmd/server"`, entry `--addr=:8091`
- Run: `cd ~/projects/aiapi && air`

### 3. air config for claude-orchestrator (NATS credentials)
- Migrated deprecated `bin`/`full_bin` fields to `entrypoint`/`args_bin`
- Hardcoded NATS creds path in `.air.toml` (dev-only, not committed)
- Run: `cd ~/projects/claude-orchestrator && air`

### 4. VSCode tasks
Added to `.vscode/tasks.json`:
- **Dev: Web** — `npm run dev` in `./web`
- **Build: Desktop** — `bash build-desktop.sh`

### 5. Desktop app (Wails v2)
- Created `cmd/desktop/main.go` + `cmd/desktop/app.go`
- Embeds the built web frontend (`cmd/desktop/frontend/dist/`) in the binary
- Starts an **embedded NATS server** (no external NATS required) on `127.0.0.1:4222`
- Starts the lucode HTTP API server on `:8090` in-process
- Opens a native window (WebKit2GTK on Linux, WebView2 on Windows) serving the UI
- `cmd/desktop/frontend/` is gitignored (populated by the build script)
- Created `build-desktop.sh`:
  1. `cd web && npm run build`
  2. Copies `web/dist/` → `cmd/desktop/frontend/dist/`
  3. `go build -tags webkit2_41 -o ./tmp/desktop ./cmd/desktop`
- Binary produced at `./tmp/desktop`

### 6. Dependencies added
- `github.com/wailsapp/wails/v2 v2.12.0`
- `github.com/nats-io/nats-server/v2 v2.12.7`

### 7. System dependency
- Installed `libwebkit2gtk-4.1-dev` (Ubuntu 24.04+ renamed it from 4.0)
- Build tag `webkit2_41` required: `go build -tags webkit2_41 ./cmd/desktop`

---

## Pending / Known gaps

### aiapi not auto-started by desktop app
The desktop app assumes `aiapi` is running at `http://localhost:8091`. It does NOT start aiapi automatically. Options:
- Bundle aiapi as a goroutine in the desktop binary (like NATS)
- Or show a "aiapi not found" warning in the UI on startup

### No working-dir picker in desktop mode
`apipkg.New(db, bus, manager, "")` passes empty `workingDir`. The workspace picker in the UI will have no base directory. Fix: read from a config file or prompt on first launch.

### Hook binary path in desktop mode
`defaultHookBinary()` looks for `./tmp/lucodehook` first (dev) then `~/.lucode/lucodehook` (prod). The desktop app should always use the prod path. Add a `lucode install`-like step to the desktop app's first-run flow.

### Windows cross-compilation
Building for Windows from Linux requires:
```bash
GOOS=windows GOARCH=amd64 go build -tags webview2 -o ./tmp/desktop.exe ./cmd/desktop
```
The `webview2` tag requires the WebView2 SDK headers — cross-compilation for Windows needs a Windows host or a cross-toolchain with WebView2 support. Easiest: build on Windows via GitHub Actions or a Windows CI runner.

### No app icon / system tray
Wails supports icons and system-tray menus via `options.App`. Not yet configured.

### NATS must be running
Both the CLI and the desktop app require an external NATS server on `nats://127.0.0.1:4222`. Start it before launching: `nats-server`
