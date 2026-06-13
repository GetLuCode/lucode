# Worktree + Checkpoint System — Status

Documento vivo del sistema de checkpoints + worktree isolation para el modelo. Se actualiza al cerrar cada phase.

---

## Estado actual: Phase 4 ✅ (cierre de fase)

El modelo trabaja en un git worktree privado, los cambios se aplican al final del turn vía 3-way merge, y los conflicts se resuelven automáticamente por el agente.

---

## Phases entregadas

### Phase 1: Filesystem tool con batched ops y head tracking
**Commit**: `1b63157` (aiapi)

- Reemplazó `write_file`/`edit_file` con un solo tool `filesystem(ops[])`
- 5 ops con discriminator: `create`, `update`, `delete`, `rename`, `copy`
- `update` soporta `replace_all` y `occurrence_index` para desambiguar matches
- Validación temprana: si algún op falla, NINGUNO se aplica (atomicidad)
- Paquete nuevo `internal/executor/fsop/`: `FSOp` struct, `Validate`, `ResolvePath`, ignore patterns, head computation
- `expected_head_checkpoint` opcional para optimistic concurrency

**Tests**: 15/15 ✅

### Phase 2: Persistent checkpoint store con revert
**Commit**: `a21d8a0` (aiapi)

- Paquete nuevo `internal/executor/checkpoint/`: CAS con blobs + trees + index
- Cada filesystem call exitoso crea un checkpoint en `<workDir>/.lucode/checkpoints/`
- HTTP API para el UI: `list`, `get`, `diff`, `revert`, `blob`
- Revert restaura el workDir al estado de un checkpoint
- 1 checkpoint por fsop call (luego esto cambia en phase 4)

**Tests**: 12/12 ✅

### Phase 3: Unified diff + pruning policy
**Commit**: `f63d522` (aiapi)

- Per-file unified diff vía `pmezard/go-difflib`
- Detección de binarios (NUL byte en primeros 8 KB)
- `PrunePolicy{MaxCheckpoints: 200, MaxAge: 30d, KeepReverts: true}`
- Pruning marca `Pruned=true` en el index y borra el tree object (no los blobs)
- 410 Gone si intentas revertir a un pruned checkpoint

**Tests**: 22/22 ✅ (5 nuevos)

### Phase 4: Worktree-based isolation con 3-way merge
**Commits**: `c09807e` (aiapi) + `8ac1a6f` (lucode)

- Paquete nuevo `internal/executor/worktree/`: `Manager` con `Ensure` (lazy create), `Path/Active`, `Cleanup`
- Passthrough mode automático para non-git dirs
- 3-way merge al final del turn vía `git merge-file -p`
- Conflict resolution: si hay conflicts, se destruye el worktree, se sintetiza un mensaje al modelo, y se corre otro agentic loop donde el modelo edita el user dir directamente
- Hasta 3 rondas de conflict resolution
- CAS checkpoint único al final del turn (reemplaza el per-fsop-call)
- **UI**: panel de Checkpoints ahora es un drawer lateral derecho que empuja el chat

**Tests**: 11/11 ✅ (nuevo paquete worktree)

---

## Total tests: 55 unit tests pasando

```
ok  aiapi/internal/api                    7/7   PASS
ok  aiapi/internal/executor/checkpoint  22/22  PASS
ok  aiapi/internal/executor/fsop        15/15  PASS
ok  aiapi/internal/executor/worktree    11/11  PASS  (NUEVO en phase 4)
```

lucode: tsc clean, build OK.

---

## Archivos creados/modificados (resumen)

### aiapi (commits en main)
```
internal/executor/
  checkpoint/                    [NUEVO package, 8 archivos]
    blob.go
    tree.go
    index.go
    store.go
    revert.go
    diff.go
    difflib.go                   [NUEVO en phase 3]
    prune.go                     [NUEVO en phase 3]
    checkpoint_test.go
  worktree/                      [NUEVO package, phase 4]
    manager.go
    apply.go
    worktree_test.go
  fsop/                           [NUEVO package, phase 1]
    fsop.go
    path.go
    ignore.go
    head.go
    apply.go
    fsop_test.go
  minimax.go                      [modificado en phase 4: apply phase + system prompt]
  tools.go                        [modificado en phases 1-4: filesystem tool + worktree integration]
  types.go
  run.go
internal/api/
  handler.go                      [modificado en phase 2: 5 endpoints nuevos para checkpoints]
  checkpoint_test.go              [NUEVO en phase 2]
```

### lucode (commits en main)
```
web/src/
  App.tsx                         [modificado en phases 3 y 4: drawer pattern]
  index.css                       [modificado en phase 4: drawer styles]
  types.ts                        [modificado en phase 2: Checkpoint types]
  api.ts                          [modificado en phases 2-3: 5 nuevos métodos]
  components/
    Checkpoints/                  [NUEVO en phase 2]
      CheckpointsView.tsx
      CheckpointsView.css
    Sidebar/
      NavPanel.tsx                [modificado en phase 2: tab "Checkpoints"]
```

---

## Flujo end-to-end (después de phase 4)

```
1. User submitea mensaje
2. aiapi crea worktree (lazy, en el primer filesystem call):
   <workDir>/.lucode/worktree/<session>/
3. Modelo corre. Sus filesystem calls van al worktree.
4. Turn termina → runApplyPhase:
   a. 3-way merge worktree → userWorkDir vía `git merge-file -p`
   b. Si no hay conflicts: CAS checkpoint + return
   c. Si hay conflicts:
      - Destroy worktree
      - Synth mensaje: "Conflict resolution needed. Files: ..."
      - Otro agentic loop (modelo edita user dir directo)
      - Re-check markers, hasta 3 rondas
5. User ve en `git status` los cambios del modelo (sin commit)
6. User decide: commit, editar más, o Revert via panel
```

---

## Decisiones de diseño locked-in

| Decisión | Por qué |
|---|---|
| Worktree lazy (no pre-emptive) | Si el modelo solo razona sin tocar archivos, no hay overhead |
| 3-way merge vía `git merge-file -p` | Standard, battle-tested, maneja todos los edge cases |
| Modelo es el conflict resolver | Es inteligente, ve ambos lados via markers, combina lógicamente |
| Sin "Apply" button — transparente al user | Como trabajar con un teammate, no un button de "submit" |
| Sin git commit en el apply | El user decide cuándo commitear; su git history queda limpia |
| Per-turn checkpoint (no per-fsop-call) | Menos ruido, el modelo puede iterar dentro del worktree libremente |
| Conflict markers en user dir si falla la resolución | El user puede ver y resolver manualmente |
| `PrunePolicy` con defaults razonables | 200 checkpoints, 30 días, reverts siempre kept |

---

## TODO / Deferred (phase 5+)

### Bloqueante para production
- [ ] **GC de blobs unreferenced** — `prune.go` borra tree objects pero los blobs quedan. Un blobs GC pass debería walkear el index y borrar blobs no referenciados por ningún kept checkpoint tree.
- [ ] **Test E2E con mock de MiniMax** — actualmente solo unit tests. Necesitamos un test que ejecute un turn completo (mock del HTTP a MiniMax) y verifique que el worktree lifecycle + apply funcionan end-to-end.
- [ ] **Lock para multi-agent concurrente** — si dos sesiones corren en paralelo, ambas creando worktrees sobre el mismo workDir, pueden pisarse. Necesita un mutex o un lock por workDir.
- [ ] **Denylist de git commands en run_command** — el modelo no debería poder `git push`, `git pull`, `git fetch`, `git merge` desde el worktree (puede romper el flow).

### Nice-to-have
- [ ] **Botón "Open Checkpoints" en el chat header** — actualmente solo se abre desde el NavPanel
- [ ] **Animación de entrada para nuevos checkpoints** — la lista aparece directo, sin highlight del último
- [ ] **Indicador de "live vs applied"** — saber qué checkpoints están en main vs solo en worktree
- [ ] **Pre-merge diff view** — antes de aplicar, mostrar al user lo que va a cambiar
- [ ] **Syntax highlighting en el unified diff** del panel
- [ ] **Tamaños de archivo en la lista de checkpoints** — el user puede ver "este checkpoint es 5 MB" antes de hacer diff

### Cleanup
- [ ] **Quitar `head_checkpoint` y `expected_head_checkpoint` del wire format** — están en el API pero ya no son necesarios en el modelo worktree. El modelo no necesita checkear staleness porque el apply phase lo maneja.
- [ ] **El per-fsop-call checkpoint en tools.go es ahora redundante** — `runApplyPhase` ya captura uno al final. Borrar el de tools.go reduce el ruido en el CAS.
- [ ] **System prompt menciona worktree explícitamente** — actualmente dice "scratch directory" pero podría ser más claro sobre lo que el modelo no ve.

---

## Comandos de testing

```bash
# aiapi
cd C:/projects/aiapi
go test ./internal/...                              # all unit tests
go test ./internal/executor/worktree/ -v            # worktree-specific
go vet ./...

# lucode
cd C:/projects/claude-orchestrator
cd web && ./node_modules/.bin/tsc --noEmit        # type-check
cd web && npm run build                            # build

# Manual smoke test (with aiapi + NATS running):
# 1. Open a session in lucode
# 2. Ask the model to edit a file
# 3. Check `git status` in the user's workDir — should show modifications
# 4. Open the Checkpoints drawer — should see the new checkpoint
# 5. Click "Revert" on a checkpoint — should restore the file
```

---

## Decisiones descartadas (para referencia)

| Idea | Por qué no |
|---|---|
| Branching (múltiples approaches en paralelo) | Overkill para v1; un solo worktree lineal es más simple |
| Reemplazar CAS con git storage | El CAS es la fuente de verdad, git es solo transport |
| Per-fsop-call checkpoints | Cambió a per-turn en phase 4 |
| Auto-commit al apply | El user debe decidir cuándo commitear |
| "Apply" button en el UI | El apply es invisible al user, como un teammate |
| Modelo con memoria de worktree | El worktree es ephemeral; el modelo no necesita saber que existe |
| Sub-agents con worktrees propios | Una sesión = un worktree es suficiente para v1 |
| GC de blobs en cada apply | Caro; mejor como un sweep periódico separado |

---

## Última actualización: 2026-06-11 (post phase 4)
