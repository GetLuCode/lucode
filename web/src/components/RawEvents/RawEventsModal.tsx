import { useState, useMemo, useEffect } from 'react'
import { useStore, rawEventsBuf } from '../../store'
import type { RawEventEntry, WSEvent } from '../../types'

// ── Syntax highlight ──────────────────────────────────────────────────────────

function highlight(json: string): string {
  return json
    .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
    .replace(
      /("(\\u[\da-fA-F]{4}|\\[^u]|[^\\"])*"(\s*:)?|\b(true|false|null)\b|-?\d+(?:\.\d*)?(?:[eE][+-]?\d+)?)/g,
      match => {
        if (/^"/.test(match)) {
          if (/:$/.test(match)) return `<span class="rjk">${match}</span>`
          return `<span class="rjs">${match}</span>`
        }
        if (/true|false/.test(match)) return `<span class="rjb">${match}</span>`
        if (/null/.test(match))       return `<span class="rjnull">${match}</span>`
        return `<span class="rjn">${match}</span>`
      }
    )
}

// ── Source badge ──────────────────────────────────────────────────────────────

function getSource(type: string): { label: string; cls: string } {
  if (type === 'stream_event')                                    return { label: 'CLAUDE', cls: 'raw-evt__badge--claude' }
  if (type === 'user_message')                                    return { label: 'UI',     cls: 'raw-evt__badge--ui' }
  if (type === 'tool_execution' || type === 'tool_execution_complete' ||
      type === 'hook_pending'   || type === 'hook_resolved')      return { label: 'TOOL',   cls: 'raw-evt__badge--tool' }
  return { label: 'SRV', cls: 'raw-evt__badge--srv' }
}

function getInnerType(entry: RawEventEntry): string {
  const ev = entry.event as WSEvent & { event?: { type?: string } }
  if (ev.type === 'stream_event' && ev.event) {
    const inner = ev.event as Record<string, unknown>
    if (inner?.type) return `stream · ${inner.type}`
  }
  if (ev.type === 'hook_pending')   return `hook · ${(ev as any).tool_name}`
  if (ev.type === 'hook_resolved')  return `hook · ${(ev as any).decision}`
  if (ev.type === 'tool_execution') return `tool · ${(ev as any).tool_name}`
  return ev.type
}

// ── Single event row ──────────────────────────────────────────────────────────

function EventRow({ entry }: { entry: RawEventEntry }) {
  const [open, setOpen] = useState(false)
  const src = getSource(entry.event.type)
  const label = getInnerType(entry)
  const ts = new Date(entry.timestamp).toLocaleTimeString('en-US', { hour12: false, hour: '2-digit', minute: '2-digit', second: '2-digit' })

  const prettyJson = useMemo(() => {
    try { return JSON.stringify(entry.event, null, 2) }
    catch { return String(entry.event) }
  }, [entry.event])

  return (
    <div className={`raw-evt${open ? ' raw-evt--open' : ''}`}>
      <div className="raw-evt__header" onClick={() => setOpen(v => !v)}>
        <span className="raw-evt__toggle">{open ? '▼' : '▶'}</span>
        <span className={`raw-evt__badge ${src.cls}`}>{src.label}</span>
        <span className="raw-evt__type">{label}</span>
        <span className="raw-evt__ts">{ts}</span>
      </div>
      {open && (
        <div className="raw-evt__body">
          <pre
            className="raw-evt__pre"
            dangerouslySetInnerHTML={{ __html: highlight(prettyJson) }}
          />
        </div>
      )}
    </div>
  )
}

// ── Modal ─────────────────────────────────────────────────────────────────────

export function RawEventsModal() {
  const modal = useStore(s => s.rawEventsModal)
  const revision = useStore(s => modal ? (s.rawEventsRevision[modal.sessionId] ?? 0) : 0)
  const closeRawEvents = useStore(s => s.closeRawEvents)
  const [activeTurn, setActiveTurn] = useState<number | 'all'>('all')

  // Sync activeTurn with the modal's initial turnIndex
  useEffect(() => {
    if (!modal) return
    setActiveTurn(modal.turnIndex !== undefined ? modal.turnIndex : 'all')
  }, [modal?.sessionId, modal?.turnIndex])

  // Close on Escape
  useEffect(() => {
    if (!modal) return
    const handler = (e: KeyboardEvent) => { if (e.key === 'Escape') closeRawEvents() }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [modal, closeRawEvents])

  // Read from mutable buffer — revision triggers re-render
  void revision
  const allEvents: RawEventEntry[] = modal ? (rawEventsBuf[modal.sessionId] ?? []) : []

  const turns = useMemo(() => {
    const set = new Set<number>()
    allEvents.forEach(e => set.add(e.turnIndex))
    return Array.from(set).sort((a, b) => a - b)
  }, [allEvents.length]) // eslint-disable-line react-hooks/exhaustive-deps

  if (!modal) return null

  const filtered = activeTurn === 'all'
    ? allEvents
    : allEvents.filter(e => e.turnIndex === activeTurn)

  const shortId = modal.sessionId.slice(0, 8)

  return (
    <div className="raw-events-overlay" onClick={closeRawEvents}>
      <div className="raw-events-panel" onClick={e => e.stopPropagation()}>

        {/* Header */}
        <div className="raw-events-panel__header">
          <div className="raw-events-panel__title">
            <span className="raw-events-panel__icon">⚡</span>
            Raw WS Events
            <span className="raw-events-panel__session-id">{shortId}…</span>
          </div>
          <div className="raw-events-panel__meta">{filtered.length} events</div>
          <button className="raw-events-panel__close" onClick={closeRawEvents} title="Close (Esc)">×</button>
        </div>

        {/* Turn filter tabs */}
        {turns.length > 1 && (
          <div className="raw-events-panel__tabs">
            <button
              className={`raw-events-panel__tab${activeTurn === 'all' ? ' raw-events-panel__tab--active' : ''}`}
              onClick={() => setActiveTurn('all')}
            >
              All
            </button>
            {turns.map(t => (
              <button
                key={t}
                className={`raw-events-panel__tab${activeTurn === t ? ' raw-events-panel__tab--active' : ''}`}
                onClick={() => setActiveTurn(t)}
              >
                Turn {t + 1}
              </button>
            ))}
          </div>
        )}

        {/* Events list */}
        <div className="raw-events-panel__list">
          {filtered.length === 0 ? (
            <div className="raw-events-panel__empty">No events yet</div>
          ) : (
            filtered.map(entry => <EventRow key={entry.id} entry={entry} />)
          )}
        </div>

      </div>
    </div>
  )
}
