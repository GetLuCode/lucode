import { useState } from 'react'
import { IconExternalLink, IconHistory, IconSearch } from '@tabler/icons-react'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import * as api from '../../api'
import type { AIModeConversation, Session } from '../../types'

function ago(iso: string): string {
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return ''
  const m = Math.round((Date.now() - t) / 60000)
  if (m < 1) return 'now'
  if (m < 60) return `${m} min ago`
  const h = Math.round(m / 60)
  if (h < 24) return `${h} h ago`
  return new Date(iso).toLocaleDateString()
}

// The history of the Google AI Mode conversations of this project: pick one to continue it.
// Its chat comes back with its messages and the next message goes to the same Google page.
export function AIModeHistory({ projectId, onContinue }: { projectId: string; onContinue: (s: Session) => void }) {
  const [open, setOpen] = useState(false)
  const [items, setItems] = useState<AIModeConversation[] | null>(null)
  const [error, setError] = useState('')
  const [q, setQ] = useState('')

  const load = () => {
    setError('')
    api.listAIModeConversations(projectId).then(setItems).catch(e => { setItems([]); setError(String(e?.message ?? e)) })
  }
  const pick = async (c: AIModeConversation) => {
    setError('')
    try {
      const s = await api.continueAIModeConversation(c.id)
      setOpen(false)
      onContinue(s)
    } catch (e) {
      setError(String((e as Error)?.message ?? e))
    }
  }

  return (
    <Popover open={open} onOpenChange={v => { setOpen(v); if (v) { setQ(''); load() } }}>
      <PopoverTrigger asChild>
        <button className="orch__icon-btn" title="Google AI Mode conversations" aria-label="Google AI Mode conversations">
          <IconHistory size={16} />
        </button>
      </PopoverTrigger>
      <PopoverContent side="bottom" align="end" className="aimode-history">
        <div className="aimode-history__title">AI Mode conversations</div>
        <div className="orch-history__search">
          <IconSearch size={13} />
          <input value={q} onChange={e => setQ(e.target.value)} placeholder="Search…" />
        </div>
        {error && <div className="aimode-history__error">{error}</div>}
        {items === null && <div className="aimode-history__empty">Loading…</div>}
        {items !== null && items.length === 0 && !error && (
          <div className="aimode-history__empty">None yet. Each conversation you have with Google AI Mode is saved here by its address.</div>
        )}
        {(items ?? []).filter(c => !q.trim() || (c.title || 'Untitled').toLowerCase().includes(q.trim().toLowerCase())).map(c => (
          <div key={c.id} className={`aimode-history__row${c.current ? ' aimode-history__row--current' : ''}`}>
            <button className="aimode-history__main" onClick={() => pick(c)} title="Continue this conversation">
              <span className="aimode-history__name">{c.title || 'Untitled'}</span>
              <span className="aimode-history__meta">
                {ago(c.updated_at)}{c.session_exists ? ` · ${c.message_count} messages` : ' · chat deleted'}{c.current ? ' · open now' : ''}
              </span>
            </button>
            <a className="aimode-history__link" href={c.url} target="_blank" rel="noreferrer" title="Open the conversation address">
              <IconExternalLink size={14} />
            </a>
          </div>
        ))}
      </PopoverContent>
    </Popover>
  )
}
