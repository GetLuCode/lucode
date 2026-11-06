# Hook: lucodehook

`lucodehook` (`cmd/lucodehook`) es el binario que `claude -p` ejecuta en cada `PreToolUse`. En modo
headless no hay dialogo de permisos y `PermissionRequest` no se dispara, asi que este evento es el
unico punto donde parar una herramienta. Lo lanza el claude del runner; quien decide es el hub.

## Camino de una aprobacion

```
claude (runner) -> lucodehook -> NATS request lucode.permissions.<sesion> -> hub
                                                                              |
claude <- JSON permissionDecision <- lucodehook <- respuesta allow/deny <-----+
```

En el hub, `Bus.handlePermissionRequest` atiende cada peticion en su propia goroutine (una suscripcion
entrega de una en una y esto espera a una persona) y `Manager.onPermissionRequest` decide, en orden:

1. Herramientas de tarea y `ToolSearch`: se permiten.
2. Ya se denego algo en este turno: se deniega, para que el agente pare y pregunte.
3. Reglas de permisos (`internal/permissions`): allow y deny deciden; ask va directo a la persona.
4. `Manager.ToolGate`: los flujos del proyecto con el disparador Tool requested (`internal/flows/toolgate.go`,
   ver `docs/flows.md`). Pueden contestar allow, deny o dejarlo abierto.
5. Si claude corre en modo auto (`permission_mode` que reporta el hook): el hub contesta `auto`, el hook
   sale sin decision y decide el clasificador de claude.
6. Si no, la persona: `hook_pending` a la pagina, hasta contestar o agotar el tiempo.

Cada llamada no contestada por una persona queda en el chat como mensaje `tool_execution`, con sus
parametros, la decision y quien la tomo (`Manager.logToolCall`). Las de una persona salen como mensaje `hook`.

## Tiempos

| Quien | Cuanto | Al agotarse |
|---|---|---|
| Flujos | 60 s | Pregunta a la persona |
| Hub, desde que el hook pregunto | 4 min menos 5 s | Deny, "user did not respond"; la pagina deja de esperar |
| Hook | 4 min | Deny, "approval timed out" |
| Hook, si nadie escucha (hub caido) | Reintenta 30 s | Deny, "the lucode hub is not running" |

## Si algo falla

El hook falla cerrado cuando esta bajo lucode: sin NATS, sin respuesta o con una respuesta invalida,
deniega, tambien en modo auto. Sin `LUCODE_SESSION_ID` no esta bajo lucode y sale sin decidir nada.

## Instalacion

- Cada SO embebe su binario: `cmd/lucode-runner/hook_<os>.go` + `embedded/lucodehook-<os>`, generado por
  `./build-runner.sh`. Un cambio en `cmd/lucodehook` exige recompilar y reiniciar el runner.
- `session.InstallHookSettings` lo escribe en `~/.lucode` junto a un settings que se pasa con `claude --settings`.

## Variables y registro

- `LUCODE_SESSION_ID` (o `ORCHESTRATOR_SESSION_ID`), `LUCODE_NATS_URL` (por defecto
  `nats://127.0.0.1:4222`) y `LUCODE_NATS_CREDS`.
- Registro en `~/.lucode/lucodehook.log`, en la maquina donde corre el hook.

## Pruebas

- `internal/api/toolgate_e2e_test.go`: el binario real del hook, NATS temporal y hub con motor.
- `internal/flows/toolgate_test.go`: reglas del flujo. `cmd/lucodehook/main_test.go`: reintento sin hub.
- `internal/natsbus/permission_test.go`: una aprobacion pendiente no frena a otras sesiones.
