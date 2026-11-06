import { useEffect, useState } from 'react'
import { IconExternalLink, IconHistory, IconSearch } from '@tabler/icons-react'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import * as api from '../../api'
import type { AIModeConversation, OrchestratorChat, Session } from '../../types'

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

// A row of the history, whichever mode it comes from.
interface Entry {
  id: string
  sessionId: string
  title: string
  searchText: string
  meta: string
  url?: string // only Google AI Mode conversations have an address of their own
  open: () => Promise<Session>
}

const aiModeEntry = (c: AIModeConversation): Entry => ({
  id: c.id,
  sessionId: c.session_id,
  title: c.title || 'Untitled',
  searchText: c.title || 'Untitled',
  meta: `${ago(c.updated_at)}${c.session_exists ? ` · ${c.message_count} messages` : ' · chat deleted'}`,
  url: c.url,
  open: () => api.continueAIModeConversation(c.id),
})

const supervisorEntry = (c: OrchestratorChat): Entry => ({
  id: c.id,
  sessionId: c.id,
  title: c.name || c.preview || 'Untitled',
  searchText: `${c.name}\n${c.preview}`,
  meta: `${ago(c.last_active)} · ${c.message_count} messages`,
  open: () => api.openOrchestratorChat(c.id),
})

// The conversation a mode comes back to: its latest one. A Google conversation held by an
// ordinary chat does not count, because opening it would leave the panel.
export async function latestConversation(projectId: string, gemini: boolean): Promise<Entry | null> {
  if (gemini) {
    const c = ((await api.listAIModeConversations(projectId)) ?? []).find(x => !x.session_exists || x.session_kind === 'orchestrator')
    return c ? aiModeEntry(c) : null
  }
  const [c] = (await api.listSupervisorChats(projectId)) ?? []
  return c ? supervisorEntry(c) : null
}

interface HistoryProps {
  projectId: string
  gemini: boolean // the panel's mode decides which conversations are listed
  currentId: string | null // the chat the panel shows now
  disabled: boolean // a turn is running: switching chats would stop it
  reloadKey: string // the list is fetched again when this changes
  onContinue: (s: Session) => void
}

function ConversationList({ projectId, gemini, currentId, disabled, reloadKey, onContinue }: HistoryProps) {
  const [items, setItems] = useState<Entry[] | null>(null)
  const [error, setError] = useState('')
  const [q, setQ] = useState('')

  useEffect(() => { setItems(null) }, [projectId, gemini])
  useEffect(() => {
    let cancelled = false
    const load = gemini
      ? api.listAIModeConversations(projectId).then(l => (l ?? []).map(aiModeEntry))
      : api.listSupervisorChats(projectId).then(l => (l ?? []).map(supervisorEntry))
    load
      .then(l => { if (!cancelled) { setItems(l); setError('') } })
      .catch(e => { if (!cancelled) { setItems([]); setError(String(e?.message ?? e)) } })
    return () => { cancelled = true }
  }, [projectId, gemini, reloadKey])

  const pick = async (e: Entry) => {
    if (e.sessionId === currentId) return
    setError('')
    try {
      onContinue(await e.open())
    } catch (err) {
      setError(String((err as Error)?.message ?? err))
    }
  }

  const needle = q.trim().toLowerCase()
  const shown = (items ?? []).filter(e => !needle || e.searchText.toLowerCase().includes(needle))

  return (
    <>
      <div className="orch-history__search">
        <IconSearch size={13} />
        <input value={q} onChange={e => setQ(e.target.value)} placeholder="Search…" aria-label="Search conversations" />
      </div>
      {error && <div className="aimode-history__error">{error}</div>}
      <div className="orch-history__list">
        {items === null && <div className="aimode-history__empty">Loading…</div>}
        {items !== null && items.length === 0 && !error && (
          <div className="aimode-history__empty">
            {gemini
              ? 'None yet. Each conversation you have with Google AI Mode is saved here by its address.'
              : 'None yet. Each conversation you have with the Supervisor is saved here.'}
          </div>
        )}
        {items !== null && items.length > 0 && shown.length === 0 && <div className="aimode-history__empty">No matches.</div>}
        {shown.map(e => {
          const current = e.sessionId === currentId
          return (
            <div key={e.id} className={`aimode-history__row${current ? ' aimode-history__row--current' : ''}`}>
              <button
                className="aimode-history__main"
                onClick={() => pick(e)}
                disabled={disabled && !current}
                aria-current={current || undefined}
                title={current ? 'Open now' : disabled ? 'Stop the current turn to switch conversation' : 'Continue this conversation'}
              >
                <span className="aimode-history__name">{e.title}</span>
                <span className="aimode-history__meta">{e.meta}{current ? ' · open now' : ''}</span>
              </button>
              {e.url && (
                <a className="aimode-history__link" href={e.url} target="_blank" rel="noreferrer" title="Open the conversation address">
                  <IconExternalLink size={14} />
                </a>
              )}
            </div>
          )
        })}
      </div>
    </>
  )
}

const historyTitle = (gemini: boolean) => (gemini ? 'AI Mode conversations' : 'Supervisor conversations')

// The Supervisor tab has room for the history beside the chat.
export function HistorySidebar(props: HistoryProps) {
  return (
    <aside className="orch-sidebar" aria-label={historyTitle(props.gemini)}>
      <div className="aimode-history__title">{historyTitle(props.gemini)}</div>
      <ConversationList {...props} />
    </aside>
  )
}

// The side panel and phone widths do not: there the history opens from a button.
export function HistoryPopover(props: HistoryProps) {
  const [open, setOpen] = useState(false)
  const title = historyTitle(props.gemini)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button className="orch__icon-btn" title={title} aria-label={title}>
          <IconHistory size={16} />
        </button>
      </PopoverTrigger>
      <PopoverContent side="bottom" align="end" className="aimode-history">
        <div className="aimode-history__title">{title}</div>
        {open && <ConversationList {...props} onContinue={s => { setOpen(false); props.onContinue(s) }} />}
      </PopoverContent>
    </Popover>
  )
}
