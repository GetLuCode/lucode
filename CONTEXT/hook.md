# Hook Binary: lucodehook

## Overview

**lucodehook** es un binario Go compilado que Claude Code invoca sincronizamente en cada evento `PreToolUse`. Actúa como puente entre el CLI de claude y el backend del orchestrator: se conecta via NATS pub/sub, envía la información de la herramienta, espera la decisión de aprobación/denegación del usuario, y sale con 0 (permitir) o 2 (denegar). Claude bloquea hasta que el hook sale.

## Architecture

```
┌─ lucode (CLI principal) ─────────────────────────┐
│  • Inicia servidor NATS (TCP :5222 + WS :5224) │
│  • Arranca manager de sesiones                  │
│  • Expone API HTTP (:8090) + Web UI             │
└──────────────────────────────────────────────────┘
                           ▲
                           │ WS (browsers)
                           │
┌─ Claude Code CLI ────────┼───────────────────────┐
│  • Inyecta env: LUCODE_SESSION_ID, LUCODE_NATS_URL
│  • En PreToolUse → ejecuta lucodehook           │
└──────────────────────────┼───────────────────────┘
                           │
                    ┌──────▼────────┐
                    │  lucodehook   │
                    │ (proc. hook)  │
                    │ • Lee stdin   │
                    │ • Conecta NATS│
                    │ • Espera aprob│
                    │ • Exit 0 o 2  │
                    └───────────────┘
```

## Generation / Embedding

El binario `lucodehook` se genera y **embebe** en el ejecutable `lucode` en tiempo de compilación:

1. **Compilar lucodehook** → `cmd/lucodehook/main.go` → `cmd/lucode/embedded/lucodehook`
   ```bash
   GOOS=linux GOARCH=amd64 go build -o cmd/lucode/embedded/lucodehook ./cmd/lucodehook
   ```

2. **Embeber en lucode** → `cmd/lucode/main.go`
   ```go
   //go:embed embedded/lucodehook
   var embeddedHook []byte
   ```

3. **Instalar** (comando `lucode install`) → extrae `embeddedHook` a `~/.lucode/lucodehook`
   ```bash
   lucode install  # → writes lucodehook a ~/.lucode/lucodehook
   ```

4. **Registrar en settings.json** → se escribe automáticamente durante install
   ```json
   {
     "hooks": {
       "PreToolUse": [
         {
           "matcher": "",
           "hooks": [{
             "type": "command",
             "command": "/home/homebox/.lucode/lucodehook"
           }]
         }
       ]
     }
   }
   ```

## Environment Variables

Inyectadas por `session.Manager` (ver `process.go`) cuando crea el proceso Claude:

- **LUCODE_SESSION_ID** (o fallback ORCHESTRATOR_SESSION_ID): ID único de la sesión
- **LUCODE_NATS_URL**: URL del servidor NATS para comunicación pub/sub
  - Formato: `nats://host:port` (TCP) o `ws://host:port` (WebSocket)
  - Default: `nats://127.0.0.1:4222`

## Stdin Format

Claude pasa esta información JSON en el stdin del hook:
```json
{
  "session_id": "...",
  "transcript_path": "/path/to/transcript.jsonl",
  "cwd": "/path/to/working/dir",
  "permission_mode": "default",
  "hook_event_name": "PreToolUse",
  "tool_name": "Bash",
  "tool_input": { "command": "ls -la", "description": "List files" },
  "tool_use_id": "toolu_01Q5iS3HFyLyqPhMxA985Squ"
}
```

Campos extraídos: `tool_name`, `tool_use_id`, `tool_input`.

## Approval Flow (NATS-based)

```
Claude PreToolUse
  │
  └─ runs lucodehook
       │
       ├─ read stdin (tool info)
       ├─ connect NATS (TCP o WS)
       ├─ subscribe to: hooks.{sessionID}.response.{toolUseID}
       ├─ publish hook_request to: hooks.{sessionID}.request
       │
       │  Backend receives hook_request:
       │    - stores in pending_hooks DB
       │    - updates session status → waiting_hook
       │    - broadcasts hook_pending to browser (WS)
       │
       ├─ hook waits for hook_response (4 min timeout)
       │  (otros mensajes ignorados durante espera)
       │
       │  User clicks Approve/Deny en browser:
       │    - browser envía hook_respond { tool_use_id, decision }
       │    - backend publishes hook_response a hooks.{sessionID}.response.{toolUseID}
       │
       ├─ hook recibe hook_response { decision }
       │
       ├─ decision == "deny" → exit 2
       └─ decision == "approve" → exit 0
```

## Concurrent Tool Calls

Claude puede ejecutar múltiples herramientas en paralelo. Cada call spawns un hook process separado conectado a NATS:

**Routing**: Cada hook envía su `tool_use_id` en `hook_request`. El backend lo usa para:
- Crear subscription específica: `hooks.{sessionID}.response.{toolUseID}`
- Publicar respuesta al canal exacto cuando el usuario aprueba/deniega

Frontend muestra una cola de hooks pendientes (un modal a la vez). Cada `hook_resolved` elimina una entrada.

## Timeout Handling

- **Hook timeout**: 4 minutos (`sub.NextMsg(4 * time.Minute)`)
- Si timeout → `deny("approval timed out — tool blocked")` → exit 2 (herramienta denegada automáticamente)

Backend maneja:
1. Hook se desconecta (timeout o usuario respondió)
2. Si aún pendiente → publica `hook_resolved` + `info`
3. Si ya resolvido → no-op

## Logging

Hook escribe logs a `~/.lucode/lucodehook-log-<timestamp>.txt`:
```
15:36:28.123456 === lucodehook invoked ===
15:36:28.234567 args: [/home/homebox/.lucode/lucodehook]
15:36:28.345678 LUCODE_SESSION_ID="sess_abc123..."
15:36:28.456789 LUCODE_NATS_URL="nats://127.0.0.1:4222"
15:36:28.567890 stdin: { full JSON }
15:36:28.678901 tool_name="Bash" tool_use_id="toolu_..."
15:36:28.789012 connecting to NATS nats://127.0.0.1:4222
15:36:28.890123 nats connected
15:36:28.901234 hook_request published to hooks.sess_abc123.request, waiting on hooks.sess_abc123.response.toolu_...
15:36:40.012345 received: type="hook_response" decision="approve" reason=""
15:36:40.123456 EXIT allow — user approved
```

## Safety: No Hook When Not Under Orchestrator

```go
if sessionID == "" {
    allow("no LUCODE_SESSION_ID — not running under lucode")
}
```

Si Claude Code se ejecuta directamente (no via orchestrator), `LUCODE_SESSION_ID` no se configura → hook exit 0 inmediatamente → todas las herramientas permitidas sin prompting.
