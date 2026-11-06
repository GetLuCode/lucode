import { useEffect, useState } from 'react'
import type { KanbanCard } from '../../types'
import * as api from '../../api'
import { backdropClose } from '../../lib/backdropClose'

interface Props {
  card: KanbanCard
  onClose: () => void
  onDetails: () => void
  onChanged: () => Promise<void>
}

// The server answers errors as "METHOD path → 409: text"; show just the text.
function message(e: unknown) {
  const m = e instanceof Error ? e.message : String(e)
  return m.replace(/^.*?→ \d+: /, '')
}

// A task waiting in Review: what its branch changes, whether it merges cleanly,
// and the two ways out: merge it (and finish the task) or send it to the agent.
export function ReviewModal({ card, onClose, onDetails, onChanged }: Props) {
  const [st, setSt] = useState<api.ReviewStatus | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  useEffect(() => {
    setSt(null)
    setError('')
    api.getCardReview(card.id).then(setSt).catch(e => setError(message(e)))
  }, [card.id])

  async function run(action: () => Promise<unknown>) {
    setBusy(true)
    setError('')
    try {
      await action()
      await onChanged()
    } catch (e) {
      setError(message(e))
      setBusy(false)
    }
  }

  const needsAgent = !!st && (st.conflicts || st.dirty)
  const canMerge = !!st && !st.error && !needsAgent

  return (
    <div className="kmodal-backdrop" {...backdropClose(onClose)}>
      <div className="kmodal">
        <div className="kmodal__header">
          <strong>Review: {card.title}</strong>
        </div>

        <div className="kmodal__body review">
          {!st && !error && <p className="kmodal__empty-hint">Checking the branch…</p>}
          {error && <p className="review__msg review__msg--bad">{error}</p>}
          {st && (
            <>
              <div className="review__meta">
                <code>{st.branch}</code> → <code>{st.base || '?'}</code>
                {st.ahead > 0 && <span> · {st.ahead} commit{st.ahead === 1 ? '' : 's'}</span>}
              </div>
              {st.worktree_path && <div className="review__path">{st.worktree_path}</div>}

              {st.error && <p className="review__msg review__msg--bad">{st.error}</p>}
              {!st.error && st.conflicts && (
                <p className="review__msg review__msg--bad">
                  Merging into {st.base} has conflicts{st.conflict_files.length > 0 && ' in:'}
                </p>
              )}
              {st.conflicts && st.conflict_files.length > 0 && (
                <ul className="review__files">
                  {st.conflict_files.map(f => <li key={f}>{f}</li>)}
                </ul>
              )}
              {!st.error && st.dirty && (
                <p className="review__msg review__msg--bad">The worktree has uncommitted changes.</p>
              )}
              {canMerge && st.merged && (
                <p className="review__msg review__msg--ok">Nothing left to merge. Finishing only cleans up.</p>
              )}
              {canMerge && !st.merged && (
                <p className="review__msg review__msg--ok">No conflicts: it merges cleanly into {st.base}.</p>
              )}

              {st.files.length > 0 && (
                <>
                  <div className="review__label">Changes ({st.files.length})</div>
                  <ul className="review__files">
                    {st.files.map(f => <li key={f}>{f}</li>)}
                  </ul>
                </>
              )}
            </>
          )}
        </div>

        <div className="kmodal__footer">
          <button className="kmodal__btn kmodal__btn--cancel" onClick={onDetails} disabled={busy}>Task</button>
          <button className="kmodal__btn kmodal__btn--cancel" onClick={onClose} disabled={busy}>Close</button>
          {st && !st.error && needsAgent && (
            <button
              className="kmodal__btn kmodal__btn--save"
              disabled={busy || !card.agent_id}
              title={card.agent_id ? undefined : 'The task has no agent'}
              onClick={() => run(() => api.resolveCardReview(card.id))}
            >
              {busy ? 'Sending…' : st.conflicts ? 'Send to agent to resolve' : 'Send to agent to commit'}
            </button>
          )}
          {canMerge && (
            <button className="kmodal__btn kmodal__btn--save" disabled={busy} onClick={() => run(() => api.mergeCardReview(card.id))}>
              {busy ? 'Merging…' : `Merge into ${st!.base} and finish`}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
