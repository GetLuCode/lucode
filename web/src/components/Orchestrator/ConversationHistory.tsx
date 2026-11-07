import { useEffect, useState } from 'react'
import { IconExternalLink, IconHistory, IconSearch } from '@tabler/icons-react'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import * as api from '../../api'
import { safeHttpUrl } from '../../lib/plugins'
import type { OrchestratorChat, PluginConversationContribution, PluginConversationRow, Session } from '../../types'

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

// Where the list comes from: the Supervisor's conversations, or the list a plugin contributes.
export type HistorySource = { kind: 'supervisor' } | { kind: 'plugin'; conversations: PluginConversationContribution; page?: boolean }

// A row of the history, whichever source it comes from.
interface Entry {
  id: string
  sessionId: string
  title: string
  searchText: string
  meta: string
  url: string | null // only if the plugin says so, and only http or https
  open: () => Promise<Session>
}

const pluginEntry = (c: PluginConversationRow, conv: PluginConversationContribution, page?: boolean): Entry => ({
  id: c.id,
  sessionId: c.session_id,
  title: c.title || 'Untitled',
  searchText: c.title || 'Untitled',
  meta: `${ago(c.updated_at)}${c.session_exists ? ` · ${c.message_count} messages` : ' · chat deleted'}`,
  url: conv.showUrl ? safeHttpUrl(c.url) : null,
  open: () => api.continuePluginConversation(conv.pluginId, c.id, page ? 'page' : undefined),
})

const supervisorEntry = (c: OrchestratorChat): Entry => ({
  id: c.id,
  sessionId: c.id,
  title: c.name || c.preview || 'Untitled',
  searchText: `${c.name}\n${c.preview}`,
  meta: `${ago(c.last_active)} · ${c.message_count} messages`,
  url: null,
  open: () => api.openOrchestratorChat(c.id),
})

// The conversation a mode comes back to: its latest one. One held by an ordinary chat does not
// count, because opening it would leave the panel.
export async function latestConversation(projectId: string, source: HistorySource): Promise<Entry | null> {
  if (source.kind === 'plugin') {
    const c = (await api.listPluginConversations(source.conversations.pluginId, projectId, source.conversations.id)).find(x => !x.session_exists || x.session_kind === 'orchestrator' || x.session_kind === 'plugin')
    return c ? pluginEntry(c, source.conversations, source.page) : null
  }
  const [c] = (await api.listSupervisorChats(projectId)) ?? []
  return c ? supervisorEntry(c) : null
}

const sourceKey = (source: HistorySource) => (source.kind === 'plugin' ? `${source.conversations.pluginId}/${source.conversations.id}/${source.conversations.model}` : 'supervisor')

interface HistoryProps {
  projectId: string
  source: HistorySource // the panel's mode decides which conversations are listed
  currentId: string | null // the chat the panel shows now
  disabled: boolean // a turn is running: switching chats would stop it
  reloadKey: string // the list is fetched again when this changes
  onContinue: (s: Session) => void
}

function ConversationList({ projectId, source, currentId, disabled, reloadKey, onContinue }: HistoryProps) {
  const [items, setItems] = useState<Entry[] | null>(null)
  const [error, setError] = useState('')
  const [q, setQ] = useState('')
  const key = sourceKey(source)

  useEffect(() => { setItems(null) }, [projectId, key])
  useEffect(() => {
    let cancelled = false
    const load = source.kind === 'plugin'
      ? api.listPluginConversations(source.conversations.pluginId, projectId, source.conversations.id).then(l => l.map(c => pluginEntry(c, source.conversations, source.page)))
      : api.listSupervisorChats(projectId).then(l => (l ?? []).map(supervisorEntry))
    load
      .then(l => { if (!cancelled) { setItems(l); setError('') } })
      .catch(e => { if (!cancelled) { setItems([]); setError(String(e?.message ?? e)) } })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId, key, reloadKey])

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
      {error && <div className="conv-history__error">{error}</div>}
      <div className="orch-history__list">
        {items === null && <div className="conv-history__empty">Loading…</div>}
        {items !== null && items.length === 0 && !error && (
          <div className="conv-history__empty">
            {source.kind === 'plugin' ? source.conversations.emptyText : 'None yet. Each conversation you have with the Supervisor is saved here.'}
          </div>
        )}
        {items !== null && items.length > 0 && shown.length === 0 && <div className="conv-history__empty">No matches.</div>}
        {shown.map(e => {
          const current = e.sessionId === currentId
          return (
            <div key={e.id} className={`conv-history__row${current ? ' conv-history__row--current' : ''}`}>
              <button
                className="conv-history__main"
                onClick={() => pick(e)}
                disabled={disabled && !current}
                aria-current={current || undefined}
                title={current ? 'Open now' : disabled ? 'Stop the current turn to switch conversation' : 'Continue this conversation'}
              >
                <span className="conv-history__name">{e.title}</span>
                <span className="conv-history__meta">{e.meta}{current ? ' · open now' : ''}</span>
              </button>
              {e.url && (
                <a className="conv-history__link" href={e.url} target="_blank" rel="noreferrer noopener" title="Open the conversation address">
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

const historyTitle = (source: HistorySource) => (source.kind === 'plugin' ? source.conversations.title : 'Supervisor conversations')

// The Supervisor tab has room for the history beside the chat.
export function HistorySidebar(props: HistoryProps) {
  return (
    <aside className="orch-sidebar" aria-label={historyTitle(props.source)}>
      <div className="conv-history__title">{historyTitle(props.source)}</div>
      <ConversationList {...props} />
    </aside>
  )
}

// The side panel and phone widths do not: there the history opens from a button.
export function HistoryPopover(props: HistoryProps) {
  const [open, setOpen] = useState(false)
  const title = historyTitle(props.source)
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button className="orch__icon-btn" title={title} aria-label={title}>
          <IconHistory size={16} />
        </button>
      </PopoverTrigger>
      <PopoverContent side="bottom" align="end" className="conv-history">
        <div className="conv-history__title">{title}</div>
        {open && <ConversationList {...props} onContinue={s => { setOpen(false); props.onContinue(s) }} />}
      </PopoverContent>
    </Popover>
  )
}
