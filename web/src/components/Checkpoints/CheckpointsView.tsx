import { useEffect, useState, useCallback } from 'react'
import { useStore } from '../../store'
import { IconHistory, IconArrowBackUp, IconFile, IconAlertCircle, IconEye } from '@tabler/icons-react'
import * as api from '../../api'
import type { Checkpoint, CheckpointChange, FileDiff } from '../../types'
import './CheckpointsView.css'

// ---- helpers -----------------------------------------------------------------

function formatTimestamp(s: string): string {
  const d = new Date(s)
  if (isNaN(d.getTime())) return s
  const now = new Date()
  const diff = now.getTime() - d.getTime()
  if (diff < 60_000) return 'just now'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`
  return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

function changeIcon(kind: string) {
  if (kind === 'added') return '+'
  if (kind === 'removed') return '−'
  return '~'
}

function changeClass(kind: string): string {
  return `ck-change ck-change--${kind}`
}

// ---- main view ---------------------------------------------------------------

export function CheckpointsView({ onClose }: { onClose?: () => void } = {}) {
  const activeProject = useStore(s => s.projects.find(p => p.id === s.activeProjectId) ?? null)
  const workDir = activeProject?.work_dirs?.[0] ?? null

  const [checkpoints, setCheckpoints] = useState<Checkpoint[]>([])
  const [head, setHead] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [selected, setSelected] = useState<string | null>(null)  // checkpoint id
  const [diffing, setDiffing] = useState<string | null>(null)     // which checkpoint is being diffed
  const [confirmRevert, setConfirmRevert] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    if (!workDir) return
    setLoading(true)
    setError(null)
    try {
      const resp = await api.listCheckpoints(workDir)
      setCheckpoints(resp.checkpoints)
      setHead(resp.head)
    } catch (e: any) {
      setError(e.message ?? String(e))
    } finally {
      setLoading(false)
    }
  }, [workDir])

  useEffect(() => {
    refresh()
  }, [refresh])

  // Auto-poll every 5s so the user sees new checkpoints without manual refresh.
  useEffect(() => {
    if (!workDir) return
    const t = setInterval(refresh, 5000)
    return () => clearInterval(t)
  }, [workDir, refresh])

  if (!activeProject) {
    return (
      <div className="ck-empty">
        <p>Select a project to view its checkpoints.</p>
      </div>
    )
  }

  if (!workDir) {
    return (
      <div className="ck-empty">
        <p>This project has no working directory. Add one in project settings to enable checkpoints.</p>
      </div>
    )
  }

  const handleRevert = async (id: string) => {
    setConfirmRevert(null)
    try {
      await api.revertToCheckpoint(id, workDir)
      await refresh()
    } catch (e: any) {
      setError(e.message ?? String(e))
    }
  }

  return (
    <div className="ck-view">
      <div className="ck-header">
        <h2><IconHistory size={18} /> Checkpoints</h2>
        <span className="ck-header__sub">workDir: <code>{workDir}</code></span>
        {onClose && (
          <button className="ck-header__close" onClick={onClose} title="Close panel" aria-label="Close">
            ×
          </button>
        )}
      </div>

      {error && <div className="ck-error"><IconAlertCircle size={14} /> {error}</div>}

      <div className="ck-layout">
        {/* Left: checkpoint list */}
        <div className="ck-list">
          {loading && checkpoints.length === 0 ? (
            <div className="ck-list__empty">Loading…</div>
          ) : checkpoints.length === 0 ? (
            <div className="ck-list__empty">
              No checkpoints yet. They appear after the first filesystem change.
            </div>
          ) : (
            [...checkpoints].reverse().map(cp => {
              const isSelected = selected === cp.id
              const isHead = cp.id === head || cp.tree === head
              return (
                <div
                  key={cp.id}
                  className={`ck-card${isSelected ? ' ck-card--selected' : ''}${cp.pruned ? ' ck-card--pruned' : ''}`}
                  onClick={() => setSelected(cp.id)}
                >
                  <div className="ck-card__row1">
                    {cp.kind === 'revert' ? (
                      <span className="ck-card__kind ck-card__kind--revert">↶ revert</span>
                    ) : (
                      <span className="ck-card__kind ck-card__kind--turn">● turn</span>
                    )}
                    {isHead && <span className="ck-card__head">head</span>}
                    {cp.pruned && <span className="ck-card__pruned">pruned</span>}
                    <span className="ck-card__time">{formatTimestamp(cp.timestamp)}</span>
                  </div>
                  <div className="ck-card__summary">{cp.summary || '(no summary)'}</div>
                  <div className="ck-card__row3">
                    <code className="ck-card__id">{cp.id.slice(0, 12)}…</code>
                    <div className="ck-card__actions">
                      <button
                        className="icon-btn"
                        title="View diff to current state"
                        onClick={e => { e.stopPropagation(); setDiffing(cp.id) }}
                      >
                        <IconEye size={14} />
                      </button>
                      <button
                        className="icon-btn"
                        title="Revert workDir to this checkpoint"
                        disabled={cp.pruned}
                        onClick={e => { e.stopPropagation(); setConfirmRevert(cp.id) }}
                      >
                        <IconArrowBackUp size={14} />
                      </button>
                    </div>
                  </div>
                </div>
              )
            })
          )}
        </div>

        {/* Right: diff view */}
        <div className="ck-detail">
          {diffing ? (
            <DiffView
              workDir={workDir}
              checkpointId={diffing}
              onClose={() => setDiffing(null)}
            />
          ) : (
            <div className="ck-detail__empty">
              <p>Select a checkpoint and click <IconEye size={14} /> to see what would change on revert.</p>
              <p>Click <IconArrowBackUp size={14} /> to revert the workDir to that state (creates a new revert checkpoint).</p>
            </div>
          )}
        </div>
      </div>

      {/* Revert confirmation modal */}
      {confirmRevert && (
        <div className="ck-modal-backdrop" onClick={() => setConfirmRevert(null)}>
          <div className="ck-modal" onClick={e => e.stopPropagation()}>
            <h3>Revert workDir?</h3>
            <p>
              This restores the workDir to the state at checkpoint
              <code> {confirmRevert.slice(0, 12)}…</code>.
              The current state becomes a new "revert" checkpoint in the history
              (so you can undo the revert).
            </p>
            <div className="ck-modal__actions">
              <button className="btn" onClick={() => setConfirmRevert(null)}>Cancel</button>
              <button className="btn btn--primary" onClick={() => handleRevert(confirmRevert)}>Revert</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

// ---- DiffView ----------------------------------------------------------------

interface DiffViewProps {
  workDir: string
  checkpointId: string
  onClose: () => void
}

function DiffView({ workDir, checkpointId, onClose }: DiffViewProps) {
  const [changes, setChanges] = useState<CheckpointChange[] | null>(null)
  const [fileDiffs, setFileDiffs] = useState<Record<string, FileDiff | null>>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [expandedPath, setExpandedPath] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    async function load() {
      setLoading(true)
      setError(null)
      try {
        const resp = await api.diffCheckpoints(checkpointId, workDir)
        if (cancelled) return
        setChanges(resp.changes)
        // Pre-fetch file diffs for each modified file.
        const diffs: Record<string, FileDiff | null> = {}
        for (const ch of resp.changes) {
          if (ch.kind === 'modified' || ch.kind === 'added' || ch.kind === 'removed') {
            try {
              const fd = await api.diffFile(ch.path, workDir, ch.from_hash ?? '', ch.to_hash ?? '')
              diffs[ch.path] = fd
            } catch {
              diffs[ch.path] = null
            }
          }
        }
        if (cancelled) return
        setFileDiffs(diffs)
      } catch (e: any) {
        if (!cancelled) setError(e.message ?? String(e))
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    load()
    return () => { cancelled = true }
  }, [workDir, checkpointId])

  if (loading) return <div className="ck-detail__loading">Computing diff…</div>
  if (error) return <div className="ck-error"><IconAlertCircle size={14} /> {error}</div>
  if (!changes || changes.length === 0) {
    return (
      <div className="ck-detail__empty">
        <p>No changes — the workDir is already at this state.</p>
        <button className="btn" onClick={onClose}>Close</button>
      </div>
    )
  }

  return (
    <div className="ck-diff">
      <div className="ck-diff__header">
        <h3>Diff: {checkpointId.slice(0, 12)}… → current</h3>
        <button className="icon-btn" onClick={onClose}>×</button>
      </div>
      <div className="ck-diff__list">
        {changes.map(ch => {
          const fd = fileDiffs[ch.path]
          const expanded = expandedPath === ch.path
          return (
            <div key={ch.path} className={changeClass(ch.kind)}>
              <div
                className="ck-diff__row"
                onClick={() => setExpandedPath(expanded ? null : ch.path)}
              >
                <span className="ck-diff__icon">{changeIcon(ch.kind)}</span>
                <IconFile size={12} />
                <span className="ck-diff__path">{ch.path}</span>
                {fd?.is_binary && <span className="ck-diff__tag">binary</span>}
                {!fd && ch.kind === 'modified' && <span className="ck-diff__tag">diff unavailable</span>}
              </div>
              {expanded && fd && !fd.is_binary && fd.unified_diff && (
                <pre className="ck-diff__unified">{fd.unified_diff}</pre>
              )}
              {expanded && fd?.is_binary && (
                <div className="ck-diff__binary">
                  Binary file changed ({fd.from_lines} → {fd.to_lines} lines)
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
