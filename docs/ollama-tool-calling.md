# Ollama Tool Calling — Protocolo y Comportamiento de gemma4:e2b

## Flujo general

El orchestrator implementa un ciclo agentico completo con Ollama. En cada turno:

1. Se envía el historial de conversación + las tool definitions al modelo
2. El modelo puede responder con texto, con `tool_calls`, o con ambos
3. Si hay `tool_calls`, el orchestrator ejecuta las tools localmente y envía los resultados
4. Se solicita una respuesta final **sin tools** para que el modelo sintetice en texto

---

## Mensajes que se envían y reciben

### Round 0 — Request inicial (con tools)

**Enviado a `POST /api/chat`:**

```json
{
  "model": "gemma4:e2b",
  "stream": true,
  "tools": [
    {
      "type": "function",
      "function": {
        "name": "list_directory",
        "description": "List files and subdirectories in a directory.",
        "parameters": {
          "type": "object",
          "properties": {
            "path": { "type": "string", "description": "Directory path to list." }
          },
          "required": ["path"]
        }
      }
    }
    // ... resto de tools (read_file, write_file, edit_file, run_command, search_files)
  ],
  "messages": [
    { "role": "system", "content": "You are a helpful coding assistant. When you need to read, write, or run code use the provided tools." },
    { "role": "user",   "content": "List the files in /tmp and tell me how many there are." }
  ]
}
```

**Recibido — stream NDJSON (chunks):**

```jsonl
{"model":"gemma4:e2b","message":{"role":"assistant","content":"","thinking":"."},"done":false}
{"model":"gemma4:e2b","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_abc123","function":{"index":0,"name":"list_directory","arguments":{"path":"/tmp"}}}]},"done":false}
{"model":"gemma4:e2b","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}
```

**Observaciones:**
- El campo `thinking` aparece en chunks separados antes de la decisión — es el razonamiento interno del modelo
- Los `tool_calls` llegan en un chunk con `done: false`, no en el chunk final
- El chunk con `done: true` tiene content vacío cuando el modelo decide usar tools
- `arguments` es un **objeto JSON**, no una string — `json.RawMessage` lo captura correctamente en Go

---

### Round 1 — Tool result + request de síntesis (SIN tools)

El orchestrator ejecuta `list_directory("/tmp")` localmente y construye el historial actualizado.

**Cómo se construye el historial en Go (`ollama.go`):**

```go
// 1. Se agrega el mensaje del assistant que pidió la tool.
//    Incluye tool_calls para que el modelo sepa qué ejecutó.
messages = append(messages, ollamaMessage{
    Role:      "assistant",
    Content:   assistantText,   // "" en la mayoría de los casos con gemma4:e2b
    ToolCalls: toolCalls,       // el mismo slice que devolvió el modelo
})

// 2. Por cada tool ejecutada, se agrega un mensaje con el resultado.
//    tool_call_id correlaciona el resultado con el tool_call del assistant.
messages = append(messages, ollamaMessage{
    Role:       "tool",
    Content:    result,   // string plano: output de os.ReadFile, exec.Command, etc.
    ToolCallID: tc.ID,    // id del tool_call que devolvió el modelo (puede ser "" si el modelo no lo emite)
})
```

**JSON que se envía a `POST /api/chat` (segundo request):**

```json
{
  "model": "gemma4:e2b",
  "stream": true,
  "messages": [
    {
      "role": "system",
      "content": "You are a helpful coding assistant. When you need to read, write, or run code use the provided tools."
    },
    {
      "role": "user",
      "content": "List the files in /tmp and tell me how many there are."
    },
    {
      "role": "assistant",
      "content": "",
      "tool_calls": [
        {
          "function": {
            "name": "list_directory",
            "arguments": { "path": "/tmp" }
          }
        }
      ]
    },
    {
      "role": "tool",
      "tool_call_id": "call_abc123",
      "content": "file1.txt (1024 bytes)\nfile2.txt (2048 bytes)\ntmpdir/\n..."
    }
  ]
}
```

**Diferencias importantes respecto a la API de Anthropic:**

| Campo | Anthropic (`tool_result`) | Ollama (`tool`) |
|---|---|---|
| `role` | `"user"` con content block `type: "tool_result"` | `"tool"` directamente |
| Correlación | `tool_use_id` obligatorio | `tool_call_id` opcional (si el modelo lo emite) |
| Content | array de content blocks | string plano |
| Identificador de tool | `tool_use_id` en el content block | `tool_call_id` en el mensaje, correlaciona con `tool_calls[].id` |

`tool_call_id` se incluye siempre que el modelo lo emita en el `tool_calls[].id`. Si el modelo no lo emite (campo vacío), el campo se omite del JSON (`omitempty`) y el modelo correlaciona por posición. Actualmente el orchestrator ejecuta tools en secuencia en el mismo orden que llegan.

> **Nota:** No se incluye `"tools": [...]` en este request — esto es intencional y crítico (ver sección siguiente).

**Recibido — stream NDJSON:**

```jsonl
{"model":"gemma4:e2b","message":{"role":"assistant","content":"There"},"done":false}
{"model":"gemma4:e2b","message":{"role":"assistant","content":" are"},"done":false}
{"model":"gemma4:e2b","message":{"role":"assistant","content":" **33**"},"done":false}
{"model":"gemma4:e2b","message":{"role":"assistant","content":" items in `/tmp`."},"done":false}
{"model":"gemma4:e2b","message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}
```

El modelo sintetiza el resultado de la tool en una respuesta textual para el usuario.

---

## El problema: loop infinito con tools en el request de síntesis

### Qué pasaba

Inicialmente el orchestrator enviaba `tools: [...]` en **todos** los requests, incluyendo el de síntesis. El comportamiento de `gemma4:e2b` al recibir tools disponibles con un resultado de tool en el historial fue:

```
Request (con tools):
  messages: [user, assistant+tool_calls, tool_result]
  tools: [list_directory, read_file, ...]

Respuesta del modelo:
  tool_calls: [{ "name": "list_directory", "arguments": {"path": "/tmp"} }]
  content: ""
```

El modelo ignoraba el resultado que ya tenía en el historial y volvía a llamar la misma tool. Esto generaba un ciclo infinito hasta llegar al límite de 10 rounds (`maxToolRounds`), momento en el que el turno terminaba sin ningún mensaje persistido para el usuario.

### Por qué ocurre

El comportamiento refleja cómo `gemma4:e2b` interpreta la presencia de tools en el contexto:

- **Con tools en el request:** el modelo está en "modo agente" — su objetivo es completar la tarea usando herramientas. Al ver un `tool_result` en el historial pero sin una respuesta textual todavía, asume que debe seguir actuando y llama otra tool.
- **Sin tools en el request:** el modelo está en "modo conversacional" — su único output posible es texto. Al ver el `tool_result` en el historial, lo procesa como información disponible y genera una respuesta en lenguaje natural.

### La solución

Enviar el request de síntesis **sin declarar tools**:

```go
// Round inicial — con tools para que el modelo pueda actuar
body, err := doOllamaChat(ctx, host, model, messages, options, withTools: true)

// Rounds de síntesis después de ejecutar tools — sin tools
nextBody, err := doOllamaChat(ctx, host, model, messages, options, withTools: false)
```

Esto no es un bug de Ollama ni del modelo — es el comportamiento esperado según la spec. La presencia de `tools` en el request es una señal explícita al modelo de que puede actuar. Quitarlas es la forma correcta de indicarle que debe responder al usuario.

---

## Resumen del protocolo implementado

```
Usuario → mensaje
         │
         ▼
Round 0: POST /api/chat  (con tools)
         │
         ├─ Responde con texto → fin, guardar respuesta
         │
         └─ Responde con tool_calls
                   │
                   ▼
            Orchestrator ejecuta tools localmente
            (read_file, write_file, run_command, etc.)
                   │
                   ▼
Round N: POST /api/chat  (SIN tools, con tool_results en historial)
         │
         └─ Responde con texto → fin, guardar respuesta
```

Las tools disponibles son ejecutadas directamente por el orchestrator en Go — no hay hook binary ni proceso externo como en el flujo de Claude CLI.
