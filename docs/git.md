# Pestana Git

Cliente de git de un proyecto: cambios, historial con grafo, comparar ramas, stash y worktrees con su tarea.

## Donde corre cada cosa
- **Runner:** todo git. `internal/api/gitclient.go` (lecturas) y `gitactions.go` (acciones), en `POST /api/git/*` por RPC.
- **Hub:** `internal/api/gitproject.go`. Nunca ejecuta git. Comprueba que el directorio es del proyecto en ese runner, reenvia la llamada y anade la tarea de cada worktree desde su base de datos.
- **Web:** `web/src/components/Git/`. El grafo (`gitGraph.ts`) y el parser de diff (`gitDiff.ts`) son funciones puras con tests en `web/tests/git.test.ts`.

## Reglas
- Un proyecto sin runner no muestra repositorio: el hub no mira su propio disco. Lo dice y manda a Environment.
- Los repositorios son los `env_dirs` del proyecto. Una ruta relativa se resuelve en el runner contra `--working-dir`, igual que en un turno. El navegador no puede pedir otra ruta (403), y un `worktree` solo se acepta si git lo lista para ese repositorio.
- Nada de lo que manda el navegador puede leerse como opcion de git: `safeRef` rechaza lo que empieza por `-` y los rangos, `safePaths` lo que sale del repositorio, y las rutas van siempre despues de `--`.
- Una respuesta de RPC de mas de 1 MB se pierde sin error. Por eso hay limites: diff 400 KB (lo avisa), 800 refs, 2000 cambios (lo avisa), 40 archivos por commit en el log; lo demas responde 413 con un mensaje. Un commit concreto trae todos sus archivos.
- Mirar no bloquea: `GIT_OPTIONAL_LOCKS=0`, para no pelear con un agente que trabaja en el mismo repositorio. La vista se refresca cada 8 s y al volver a la ventana.
- `fetch`, `pull` y `push` esperan hasta 5 minutos: `RPCRequest` respeta el plazo del contexto del que llama. Nunca piden credenciales (`GIT_TERMINAL_PROMPT=0`): sin ellas fallan con el mensaje de git. `pull` es solo `--ff-only` y `push` nunca fuerza.
- Un merge con conflictos se queda abierto, como en la consola: la pestana lo avisa y ofrece Abort. Los conflictos se arreglan en los archivos y se marcan con stage. No hay editor de conflictos.
- Lo que no se puede deshacer pide confirmacion (discard, borrar sin seguimiento, `git rm`, drop, borrar rama sin merge, quitar worktree). No se puede quitar el worktree de una tarea cuyo agente esta trabajando.

## Ancho de los paneles
- El menu izquierdo se colapsa a una columna de iconos con el boton de la barra superior. A ancho de telefono no aplica: ahi las listas ya se abren bajo demanda.
- Con un commit abierto, el divisor entre el arbol y el detalle se arrastra (tambien con las flechas y Home; doble clic lo restablece), y el boton del detalle colapsa el arbol a su minimo de 180 px. El detalle nunca baja de 380 px.
- Las filas del arbol se adaptan por container query: primero pierden chips y contadores, despues las listas de archivos. Quedan el grafo, el mensaje, el autor y la fecha.
- Ambos anchos se guardan en `localStorage`: son preferencias de cada navegador.
- El boton de altura completa del diff oculta lo que tiene encima en su panel (datos y archivos del commit, la barra de Comparar y, con los paneles apilados, la lista). `Esc` lo deshace. No se guarda: al volver a la pestana el diff esta en su altura normal. En Cambios y Stash con los paneles lado a lado no aparece, porque el diff ya ocupa toda su columna.

## Referencias al Supervisor
- En un diff, los numeros de linea se pulsan: clic elige una, Shift+clic o arrastrar eligen varias, `Esc` lo deshace. El clic derecho sobre un numero, o sobre el codigo de una linea elegida, abre el menu: copiar la referencia (`ruta:12-15 @ abc1234`), copiar el codigo o mandarla al Supervisor. Sobre el codigo, si hay texto seleccionado queda el menu del navegador. Un clic derecho fuera de la seleccion elige esa linea. Un cambio en el diff borra la seleccion: esas posiciones ya no significarian lo mismo.
- La numeracion es la de la version nueva; solo una seleccion de lineas eliminadas usa la vieja, y lo dice (`(removed lines)`).
- La ruta de un archivo se copia desde su fila o desde la cabecera del diff; desde esa cabecera tambien se manda el archivo entero al Supervisor.
- Mandar no gasta tokens: abre el panel, deja un chip junto a la caja y espera a la pregunta (`orchRefs` en el store). Al enviar, cada chip se convierte en un bloque delante del mensaje (`gitRef.ts`) con las lineas elegidas (200 como mucho), asi vale tambien en modo Gemini, que no tiene tools.
- **Tool `git_diff`** (`gitDiffTool` en `gitproject.go`): solo lectura, se aprueba sola como los demas de `Names()`. El bloque trae sus argumentos exactos. Pasa por la misma comprobacion que la pestana, asi que solo lee directorios del proyecto, y funciona aunque el Supervisor este en otro runner que el repositorio. Se corta a 60 KB. Como la lista de tools la sirve el runner, un runner anterior no lo ofrece.

## Avatares
- El navegador pide la imagen a `avatars.githubusercontent.com/u/e?email=...` (`gitAvatars.ts`), un endpoint sin documentar. **Eso manda a GitHub el correo de cada autor que aparece y la IP de quien mira**; cada direccion se pide una vez por carga de pagina.
- Una direccion sin cuenta recibe una imagen generica: se compara con la de una direccion inventada y entonces quedan las iniciales. Sin red, o si GitHub cambia el endpoint, tambien.
- El runner manda los correos: uno anterior no los envia en stash y worktrees, y ahi salen iniciales hasta actualizarlo.

## Worktree, tarea y merge
- **Tarea:** se busca por sesion (transcripcion, lo que reporto el proceso, fila de Review) y por la nota `Merged X into Y.` que deja un merge. Solo cuentan las sesiones que corrieron en ese runner.
- **Merge de un worktree vivo:** el commit mas antiguo de la linea de primer padre de la rama principal que desciende de la punta de la rama. Si la punta esta en esa linea, no hay commit de merge (`contained`: fast-forward o rama sin commits propios). Se mira `main` y, si no, lo que tenga sacado el checkout principal, porque las tareas se fusionan ahi.
- **Tarea con el worktree ya borrado:** la rama ya no existe, asi que se reconoce por el mensaje (`Merge task: <titulo>` o la rama en el mensaje). Un mensaje escrito por un Flow no se puede reconocer: entonces se muestra solo `Merged into <rama>` con la fecha de la nota, sin commit.

## Limitaciones conocidas
- Un runner anterior a esta pestana responde 404 y la pestana pide actualizarlo.
- No hay rebase interactivo, reset ni staging por lineas.
- Con busqueda por mensaje el grafo pierde la continuidad: faltan los commits intermedios.
- El log carga 150 commits por pagina, y los que no se cargaron no aparecen como destino de un salto; el detalle del commit se abre igual.
