# Flows: como debe funcionar y que puede fallar

La pestana Flows automatiza lo que pasa en el tablero con grafos de nodos. Este documento dice
como debe comportarse y como reconocer cada fallo. Como probar y como anadir nodos esta en
`CLAUDE.md`, seccion "Flows".

## Como debe funcionar

### Quien hace que

| Pieza | Donde corre | Que hace |
|---|---|---|
| Editor (litegraph) | Navegador | Solo dibuja y edita. Nunca ejecuta un grafo. |
| Motor (`internal/flows`) | Hub | Valida, decide el siguiente paso y guarda el estado en la base de datos. |
| Git status, Merge, Run agent | Runner, por NATS | Lo que toca git o ejecuta claude. |
| AI evaluate | Hub, por HTTP | Una peticion a la API de Anthropic. Sin claude, sin runner y sin disco. |
| Human approval | Base de datos | Una espera que sobrevive a reinicios. |

### De un evento a una ejecucion

1. Un cambio en una tarjeta deja un evento en `flow_events`, en la misma transaccion que el cambio.
2. El motor busca flujos publicados y activos cuyo disparador coincida, y crea una ejecucion fijada
   a la version publicada en ese momento.
3. Lo lento (git, API, agente) corre aparte y vuelve como evento. Una espera queda en la base de
   datos, asi que un reinicio del hub la retoma.

### Borrador y version publicada

- El editor guarda solo el **borrador**. Lo que se ejecuta es siempre la **version publicada**.
- Publicar valida el borrador y crea una version nueva. Las ejecuciones en curso siguen en la suya.
- Dos reglas del editor protegen el borrador: un canvas que aun no ha cargado no guarda, y un grafo
  con nodos que el hub no conoce se abre en solo lectura y tampoco guarda.

### Limites

| Limite | Valor | Al superarlo |
|---|---|---|
| Flujos encadenados (`MaxDepth`) | 8 | La ejecucion se crea fallida: "not started: ... looks like a loop" |
| Pasos por ejecucion (`MaxStepsPerRun`) | 200 | "the run reached the limit of 200 steps" |
| Turnos de agente + AI evaluate (`MaxAgentTurns`) | 10 | "the run reached the limit of 10 agent or AI turns" |

Ademas: una sola ejecucion por flujo y tarjeta (las demas esperan en cola), un flujo no reacciona a
sus propios movimientos, los bucles solo pasan por la salida `again` de Retry, y la pausa del
proyecto retiene los pasos hasta quitarla.

Un error sin salida `failed` conectada hace fallar la ejecucion: nunca termina como exito silencioso.

### Task finished: donde va una tarea al terminar

Blueprint publicado una vez en cada proyecto. Uno borrado no vuelve.

| Git status | Destino | Aviso al supervisor |
|---|---|---|
| `clean`, `conflicts`, `dirty` | Review | Si |
| `nothing` (sin rama o ya fusionada) | Done | Si |
| `failed` (no se pudo leer) | Review, con un comentario | **No** (ver "Limitaciones conocidas") |

Tiene tres Move card porque cada uno lleva una columna fija y cada camino usa el suyo.

Si ningun flujo activo tiene el disparador Task done (borrado, desactivado o proyecto en pausa), Go
deja la tarea en Review sin consultar git, porque Done podria esconder trabajo sin fusionar.

Siguen en Go: arrancar el agente al entrar en In Progress, liberar dependencias y el boton de merge
de Review.

### Aprobar herramientas: Tool requested y Tool decision

Un flujo puede contestar al hook `PreToolUse`. Solo sustituye el paso de preguntar a una persona:

```
hook del runner -> NATS -> hub:
  herramientas de tarea y ToolSearch -> se permiten
  ya se denego algo en este turno    -> se deniega
  reglas de permisos                 -> allow y deny deciden; ask va directo a la persona
  flujos con Tool requested          -> allow, deny o ask
  ask, o ningun flujo                -> agente en auto: decide el clasificador de claude
                                        los demas: la persona, como siempre
```

**Modo auto.** Un agente con `auto_mode` lanza claude con `--permission-mode=auto` y tambien con el
hook, asi que pasa por el mismo camino. Los docs de Claude Code dicen que un `allow` del hook cuenta
como una regla y se resuelve antes del clasificador; si el hook no contesta, decide el clasificador.
Nunca te pregunta, salvo una regla `ask` explicita. Un flujo puede distinguirlo con `event.auto_mode`.

**Tool log.** Cada llamada queda en el chat de la sesion con sus parametros, su decision y quien la
tomo (regla, flujo o `Claude auto mode`), tambien en agentes sin auto. Solo se registra la peticion:
no el resultado, ni lo que el clasificador decidio al final. Las que contesta una persona siguen
saliendo como su aprobacion de siempre.

**Match tool** es el mismo test que una regla de permisos, con el mismo codigo (`permissions.Matcher`):

| Campo | Ejemplo | Significado |
|---|---|---|
| Tool | `Read\|Glob\|Grep`, `mcp__github__.*`, `*` | Regex sobre el nombre completo. `*` es cualquiera. |
| Parameters | `command: ^go test ./[^;&\|]*$` | Una por linea, `parametro: regex`. Tienen que cumplirse todas. |

Las reglas de permisos ganan lo mismo: su `tool` tambien admite regex. Un nombre normal sigue
significando exactamente esa herramienta.

**Cuidado con Bash.** La regex de un parametro puede coincidir con solo una parte del texto, y un comando
puede encadenar otros: `^go test` tambien coincide con `go test && rm -rf /`. Para un allow, cierra con
`$` y excluye `;`, `&`, `|`. Una lista de patrones prohibidos tampoco es una barrera: siempre hay otra
forma de escribir el mismo comando.

El blueprint **Approve tools with rules** no se siembra: deja correr herramientas sin nadie delante, asi
que cada proyecto decide. Se crea desde el editor. No usa la API.

| Situacion | Resultado |
|---|---|
| `Read`, `Glob`, `Grep` | Allow |
| `Bash` con `rm -r`, `git push --force`, `git reset --hard` o `curl \| sh` | Deny; se paran las demas herramientas del turno |
| Lo demas | La persona, o el clasificador si el agente esta en auto |

**AI evaluate es opcional:** se puede anadir como cualquier nodo, gasta tokens de API en cada llamada que
llegue a el, y si falla se puede conectar su salida `failed` a un Tool decision `ask`.

**Reglas, porque el hook esta esperando:**

- Cualquier fallo queda sin decidir, nunca en un allow: error de expresion, flujo que termina sin
  decidir, ejecucion cancelada, proyecto en pausa, hub parandose o 60 s sin respuesta. Eso significa la
  persona, o el clasificador si el agente esta en auto.
- Si hay varios flujos, gana el mas estricto (`deny`, luego `ask`, luego `allow`). Un `deny` no espera
  a los demas y los cancela.
- Una entrada de mas de 64 KB va directa a la persona: cortarla haria que el modelo juzgue solo una
  parte de lo que se va a ejecutar.
- Los 60 s salen de los 4 minutos del hook, y la cuenta atras de la pagina ya los descuenta.
- Las ejecuciones no hacen cola entre ellas ni heredan la profundidad de la tarjeta. Se borran a los 7
  dias, porque hay una por herramienta y guardan su entrada.

Lee `event.tool_name`, `event.tool_input` (`event.tool_input.command`), `event.tool_input_text` (el
JSON tal cual), `event.work_dir`, `event.task` (titulo de la tarjeta, vacio si no hay) y `event.agent`
(nombre del agente, vacio si la sesion no tiene; por ejemplo `event.agent == "reviewer"` en un If). Tambien corre
en sesiones sin tarjeta; ahi `card.*` no existe y Comment, Add tag y Notify supervisor fallan, asi que
acaban en la persona.

### Agentes que preparan flujos

Las tools `flow_list`, `flow_get`, `flow_catalog`, `flow_create` y `flow_update` dejan a un agente leer
flujos y escribir borradores de su proyecto. El Supervisor las tiene; a otro agente hay que dárselas en
su pestana Tools, y `agent_create` no las hereda.

- **Solo una persona publica.** No hay tool para publicar, activar, desactivar, borrar ni pausar, porque
  un flujo publicado puede aprobar herramientas o fusionar ramas. Un borrador no ejecuta nada, y por eso
  estas tools se aprueban solas y no pasan por reglas ni flujos.
- **En vivo.** Un cambio del agente aparece en el editor abierto, sin mover la vista.
- **Gana el ultimo que guarda (decision del usuario).** Lo que escribe el agente sustituye lo que hay en
  el canvas, con ediciones sin guardar; y un guardado tuyo en curso puede pisar lo del agente.
- **Aviso antes de publicar.** El estado dice `Changed by <agente>: review it before publishing` hasta
  publicar. Tu guardado no lo quita, porque ese guardado tambien lleva el cambio del agente. No muestra
  las diferencias con la version publicada.
- `flow_update` rechaza nodos que el hub no conoce y grafos sin `nodes`. Lo demas puede estar a medias: el
  agente recibe la lista de problemas que impedirian publicar.

### Merge

| Opcion | Por defecto |
|---|---|
| Como: `merge`, `squash`, `ff-only` | `merge` |
| Mensaje del commit | `Merge task: <titulo>` |
| Borrar el worktree | Si |
| Borrar la rama | Si |
| Mover la tarjeta a Done | Si |
| Merge without a person | No |

- Publicar se rechaza si algun camino llega a Merge sin pasar por el `approved` de un Human approval,
  salvo que el nodo lleve "Merge without a person".
- Publicar se rechaza si se borra la rama pero se conserva el worktree: git no lo permite.
- Si la rama conflictua, hay cambios sin commit o git rechaza el merge, no cambia nada y sigue por
  `conflicts`.
- Con opciones que no son las de por defecto se usa `/api/review/merge-options`. Asi un runner viejo
  contesta 404 y no fusiona ignorandolas.

## Errores posibles

### Al publicar

El nodo afectado se marca en rojo y el mensaje sale en la pestana Node.

| Mensaje | Causa y arreglo |
|---|---|
| `Match tool matches everything: ...` | Sin herramienta ni parametros: escribe alguno. |
| `Match tool: the line "..." is not parameter: regex` | Falta el `parametro:` delante de la regex. |
| `Match tool: rule "...": invalid tool regex` o `invalid regex` | La regex no compila (sintaxis RE2 de Go). |
| `Match tool only works in a flow started by Tool requested: ...` | Sin ese disparador no hay herramienta que leer. |
| `the flow is empty` | No hay nodos. |
| `the flow needs a trigger: nothing would ever start it` | Falta un disparador. Si el flujo tenia uno, mira primero "El editor perdio nodos". |
| `unknown node type "..."` | El hub no conoce ese tipo: suele ser un hub mas viejo que el flujo. Reinicialo. |
| `... is not connected to a trigger, so it would never run` | Nodo suelto: conectalo o borralo. |
| `this node is in a loop: ...` | Ciclo fuera de Retry. Haz que pase por su salida `again`. |
| `... is a trigger: nothing can connect into it` | Una conexion entra en un disparador. |
| `... has no output "..."` | La conexion usa una salida que el nodo no tiene. |
| `Merge can be reached without a person approving it: ...` | Pon un Human approval antes o activa "Merge without a person". |
| `Merge: to delete the branch the worktree has to be deleted too, ...` | Conserva los dos o borra los dos. |
| `AI evaluate: the third output is connected but Answer 3 is empty` | Rellena Answer 3 o quita la conexion. |
| `AI evaluate: the answer "..." is there twice` | Dos respuestas iguales: cambia una. |
| `Tool decision only works in a flow started by Tool requested: ...` | Ningun otro disparador espera su respuesta. |
| `... cannot share a flow with Tool requested: ...` | Otro disparador en el mismo flujo: separalos. |
| `... cannot be used with Tool requested: ...` | Nodo lento o que arranca un turno (Run agent, Human approval, Delay, Merge...). Para preguntar a una persona usa Tool decision con `ask`. |
| `this flow has no Tool decision, ...` | Solo podria acabar preguntando a la persona. |
| `this allows every tool without checking anything: ...` | Allow sin nada antes: pon un Match tool o una condicion. Para no preguntar, usa el modo auto del agente. |

### Durante una ejecucion

El error se ve en la pestana Runs, en el paso que fallo.

| Mensaje | Causa | Que hacer |
|---|---|---|
| `... needs a card, and this run has none` | Un nodo de tarjeta en una ejecucion sin tarjeta (Manual o Schedule). | Elige una tarjeta al lanzarla o quita el nodo. |
| `this card has no branch to merge` | Merge sobre una tarjeta sin rama. | Pon Git status antes y usa su salida `nothing`. |
| `runner ... is too old for these merge options: ... Nothing was merged` | Runner anterior a las opciones de Merge. | Recompila el runner (`./build-runner.sh`) o usa las opciones por defecto. |
| `the hub stopped during this merge and the branch is not merged: ...` | El hub se reinicio a mitad de un merge y la rama sigue sin fusionar. No reintenta a ciegas. | Revisa el repositorio y lanza la ejecucion de nuevo. |
| `the hub stopped during this merge and cannot tell if it finished: ...` | Igual, pero tampoco pudo consultar al runner. | Comprueba el runner y el repositorio a mano. |
| `merged, but could not move the task to Done: ...` | El merge se hizo; fallo mover la tarjeta. | Mueve la tarjeta a mano. **No repitas el merge.** |
| `Move card: choose where to` | Move card sin destino. | Configura el nodo y publica de nuevo. |
| `no Anthropic API key: add one in the AI evaluate node` | Sin clave en `~/.lucode/flows-anthropic.json` ni `ANTHROPIC_API_KEY` en el hub. | Anade la clave. |
| `the Anthropic API rejected the key` | Clave invalida o revocada. | Cambiala. |
| `the Anthropic API is rate limiting this key; ...` | Limite de la API, tras los reintentos del SDK. | Espera o baja el uso. |
| `the model declined to evaluate this (...)` | El modelo rechazo el contenido. | Revisa el texto enviado o conecta la salida `failed`. |
| `the model stopped without an answer (...)` | La respuesta termino sin una decision. | Conecta `failed` para tratarlo; reintenta si es puntual. |
| `this approval is no longer waiting for a decision` | Ya se decidio, o la ejecucion termino o se cancelo. | Nada: recarga la campana. |

### Sintomas sin mensaje de error

| Sintoma | Causa probable | Como comprobarlo |
|---|---|---|
| Faltan nodos o solo hay una conexion | "El editor perdio nodos", abajo | Compara el borrador con la version publicada en `flows` y `flow_versions`. |
| La tarjeta no se mueve tras `task_done` | Hay un flujo activo con Task done que no la mueve, o su ejecucion fallo. | Pestana Runs de ese flujo. |
| Un flujo nunca arranca | Sin publicar, desactivado, proyecto en pausa, otro flujo en cola en esa tarjeta, o el evento lo causo el propio flujo. | Punto verde en la lista, boton de pausa y pestana Runs. |
| Ejecucion parada en `waiting` | Espera un Human approval, un turno o un evento; es lo normal. | El paso en azul y la campana. |
| Un cambio del borrador no tiene efecto | Sin publicar: se ejecuta la version publicada. | El estado dice `Saved, not published`. |
| Una herramienta pregunta a la persona con un flujo activo | El flujo no llego a decidir: fallo, tardo mas de 60 s o la entrada pasa de 64 KB. | Pestana Runs y linea `no decision on` en el log del hub. |
| Un agente nunca pasa por el flujo | Una regla ya lo decide. | Reglas de permisos. |
| Un agente en auto no deja tool log | Su runner es anterior a este cambio: lanza claude sin el hook. | Recompila y reinicia el runner. |

### El editor perdio nodos (caso real, 2026-11-06)

**Que paso.** El borrador de Task finished en el proyecto `lucode` paso de 8 nodos a 4. Se perdieron
Task done, Git status y los dos Notify supervisor. La version publicada quedo intacta.

**Cadena de causas:**

1. El hub en marcha era anterior a esos tipos de nodo, porque `air` no pudo reemplazarlo.
2. El editor se saltaba en silencio los tipos que el hub no ofrecia.
3. Al mover un nodo, el guardado automatico escribio lo que quedaba sobre el borrador.

**Arreglo.** `unknownNodeTypes` en `flowGraph.ts`: el flujo se abre en solo lectura, con un aviso que
nombra los tipos, y `saveDraft` no guarda. Esto evita perder mas nodos, pero no repara un borrador ya
danado: ese no muestra aviso, porque los nodos que le quedan son de tipos conocidos.

**Reparacion.** Con el hub ya actualizado y la pestana cerrada, copiar `flow_versions.graph` de la
ultima version a `flows.draft`. No publicar antes el borrador danado.

### Hub viejo: como reconocerlo

Sintomas: faltan tipos en la paleta, sale el aviso de nodos desconocidos, o `tmp/build-errors.log`
acumula `exit status 1`.

```powershell
Get-Process lucode,air | Select-Object Id,ProcessName,StartTime
netstat -ano | findstr :8090
```

Si el `lucode` que escucha es anterior al ultimo cambio, hay que ejecutar `taskkill /PID <pid> /F`,
comprobar que `air` lo levante y recargar la pagina: los tipos de nodo se piden una vez al abrir la
pestana.

## Limitaciones conocidas

- **Arranque antes del puerto.** `cmd/lucode/main.go` llama a `StartFlows` antes de
  `ListenAndServe`, asi que una instancia que falla con el puerto ocupado ya sembro blueprints y
  arranco su motor un instante. Por eso un flujo puede aparecer mientras sigue sirviendo un hub viejo.
  Es la explicacion mas probable del caso de arriba, sin confirmar en logs. Falta corregirlo.
- **`failed` en Task finished no avisa al supervisor**, aunque la descripcion del blueprint dice que
  se le avisa siempre.
- **`Handles` mira solo si existe el disparador.** Cualquier flujo activo con Task done evita el
  respaldo de Go. Si Task finished esta desactivado y otro flujo con ese disparador no mueve la
  tarjeta, esta se queda donde estaba.
- **Una ejecucion fallida de Task finished no tiene respaldo.** La tarjeta se queda donde estaba hasta
  que alguien la mueva: el respaldo de Go solo actua si ningun flujo va a reaccionar.
- **Un Task finished ya sembrado no se actualiza.** Un cambio en `blueprints.go` solo llega a proyectos
  nuevos; `flow_seeds` impide volver a sembrar.

- **Con AI evaluate, el modelo lee texto que puede venir de fuera.** Una entrada manipulada podria
  convencerlo de aprobar. Las reglas `deny` van antes y el modelo no las ve.
- **`Read`, `Glob` y `Grep` pasan sin mirar la ruta** en el blueprint, asi que leen tambien fuera del
  proyecto. Para evitarlo, anade `file_path: ^/tu/proyecto/` al nodo o quitalos de `Tool`.
- **Un allow del hook se salta el clasificador** en un agente en auto, salvo los casos que claude protege
  siempre: lo que un flujo aprueba ya no lo revisa nadie mas.
- **Agentes en auto dependen del hub.** Con el hub caido, el hook reintenta 30 s y deniega; antes el agente
  seguia solo. Un reinicio del hub con un hook esperando pierde la peticion y el hook deniega a los 4 minutos.
- **Una evaluacion cancelada por tiempo sigue en curso** hasta que responde la API y se paga igual.
- **El tool log no tiene el resultado de la herramienta.** Haria falta un hook `PostToolUse`.

## Sin probar

- La API real de Anthropic: solo la falsa (`internal/fakeanthropic`).
- Aprobar herramientas con un claude real y con un runner remoto: si se probo el binario real del hook
  contra un hub con NATS temporal, y su entrada imita la de claude. En particular, el campo
  `permission_mode` y que el silencio del hook llegue al clasificador salen de la documentacion.
- En navegador: los nodos Tool requested, Match tool y Tool decision, el tool log en vivo y la recarga
  del editor cuando un agente cambia un borrador.
- Las tools de flujos con un claude real por MCP: solo se llamo al handler del hub y se probo el hook.
- Task finished completo con un runner remoto. Git status y Merge si se probaron con runner real por
  NATS, cada uno por separado del blueprint.
- La app completa con WebSocket real, y el editor en movil.
- En navegador: el bloqueo de guardado y el aviso de nodos desconocidos. Solo tiene prueba la deteccion.
- El dialogo de Review con una tarjeta que llego por el respaldo y no tiene rama.
- Merge interrumpido por un reinicio: solo con un host simulado.
- Un segundo runner real.
