import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { IconDownload, IconEdit, IconTrash } from '@tabler/icons-react'
import { useStore } from '../../store'
import * as api from '../../api'
import type { KanbanCard, Session } from '../../types'

// Running: the project's current sessions (the chats of its tasks), live ones first.
export function RunningView() {
  const navigate = useNavigate()
  const sessions = useStore(s => s.sessions)
  const activeProjectId = useStore(s => s.activeProjectId)
  const activeSessionId = useStore(s => s.activeSessionId)
  const sessionStatus = useStore(s => s.sessionStatus)
  const setActiveSession = useStore(s => s.setActiveSession)
  const deleteSession = useStore(s => s.deleteSession)
  const renameSession = useStore(s => s.renameSession)

  const [menuId, setMenuId] = useState<string | null>(null)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editingName, setEditingName] = useState('')

  const [cards, setCards] = useState<Record<string, KanbanCard>>({})
  const [lists, setLists] = useState<Record<string, string>>({})
  useEffect(() => {
    if (!activeProjectId) return
    let cancelled = false
    const load = () => Promise.all([api.listKanbanCards(activeProjectId), api.listKanbanLists(activeProjectId)]).then(([cs, ls]) => {
      if (cancelled) return
      setCards(Object.fromEntries(cs.map(c => [c.id, c])))
      setLists(Object.fromEntries(ls.map(l => [l.id, l.name])))
    }).catch(() => {})
    load()
    const t = setInterval(load, 10000)
    return () => { cancelled = true; clearInterval(t) }
  }, [activeProjectId])

  const fmt = (iso?: string) => {
    if (!iso) return ''
    const d = new Date(iso)
    return isNaN(d.getTime()) || d.getFullYear() < 2000 ? '' : d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
  }

  const statusOf =(s: Session) => sessionStatus[s.id] ?? s.status
  const rank = (s: Session) => (statusOf(s) === 'idle' ? 1 : 0)
  // Only task sessions (tied to a kanban card), live ones first; the store keeps the rest by activity.
  const list = sessions.filter(s => s.card_id).sort((a, b) => rank(a) - rank(b))

  const dot = (s: Session) => {
    const st = statusOf(s)
    if (st === 'running') return '●'
    if (st === 'waiting_hook') return '◆'
    if (st === 'error') return '✕'
    return '○'
  }

  async function saveEdit() {
    if (editingId && editingName.trim()) await renameSession(editingId, editingName.trim())
    setEditingId(null)
    setEditingName('')
  }

  async function exportSession(e: React.MouseEvent, s: Session) {
    e.stopPropagation()
    setMenuId(null)
    try {
      const blob = await api.exportSession(s.id)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `${(s.name || 'session').replace(/[^a-zA-Z0-9_-]/g, '_')}.json`
      a.click()
      URL.revokeObjectURL(url)
    } catch (err) {
      console.error('export failed', err)
    }
  }

  return (
    <div className="running-view">
      {list.length === 0 ? (
        <div className="running-view__empty">No sessions yet. Move a task to In Progress to start one.</div>
      ) : (
        <ul className="running-list">
          {list.map(s => (
            <li
              key={s.id}
              className={`running-item${activeSessionId === s.id ? ' running-item--active' : ''}`}
              onClick={() => {
                setActiveSession(s.id)
                navigate(`/projects/${activeProjectId}/sessions/${s.id}`)
              }}
            >
              <span className={`status-dot status-dot--${statusOf(s)}`}>{dot(s)}</span>
              <div className="running-item__info">
                {editingId === s.id ? (
                  <input
                    className="input session-list__edit-input"
                    value={editingName}
                    onChange={e => setEditingName(e.target.value)}
                    onKeyDown={e => {
                      e.stopPropagation()
                      if (e.key === 'Enter') saveEdit()
                      if (e.key === 'Escape') { setEditingId(null); setEditingName('') }
                    }}
                    onBlur={saveEdit}
                    onClick={e => e.stopPropagation()}
                    autoFocus
                  />
                ) : (
                  <>
                    <span className="running-item__name">{s.name || 'Untitled'}</span>
                    <span className="running-item__meta">{s.message_count} msgs · {statusOf(s)}</span>
                    {(() => {
                      const c = s.card_id ? cards[s.card_id] : undefined
                      const started = fmt(c?.started_at || s.created_at)
                      const finished = fmt(c?.finished_at)
                      return (
                        <>
                          {c && (
                            <span className="running-item__meta">
                              Task: {c.title} · <span className="running-item__task-status">{lists[c.list_id] ?? '—'}</span>
                            </span>
                          )}
                          <span className="running-item__meta">
                            Started {started || '—'} · {finished ? `Finished ${finished}` : `Active ${fmt(s.last_active) || '—'}`}
                          </span>
                          {(s.branch || s.worktree) && (
                            <span className="running-item__meta running-item__mono" title={s.worktree}>
                              {s.branch && <>⎇ {s.branch}</>}{s.branch && s.worktree && ' · '}{s.worktree}
                            </span>
                          )}
                        </>
                      )
                    })()}
                  </>
                )}
              </div>
              <div className="nav-tree__menu-container">
                <button
                  className="icon-btn nav-tree__action-btn"
                  onClick={e => { e.stopPropagation(); setMenuId(menuId === s.id ? null : s.id) }}
                  title="Session menu"
                >⋯</button>
                {menuId === s.id && (
                  <div className="nav-tree__dropdown">
                    <button
                      className="nav-tree__menu-item"
                      onClick={e => { e.stopPropagation(); setEditingId(s.id); setEditingName(s.name); setMenuId(null) }}
                    >
                      <IconEdit size={14} />
                      <span>Rename</span>
                    </button>
                    <button className="nav-tree__menu-item" onClick={e => exportSession(e, s)}>
                      <IconDownload size={14} />
                      <span>Export</span>
                    </button>
                    <button
                      className="nav-tree__menu-item nav-tree__menu-item--danger"
                      onClick={e => { e.stopPropagation(); deleteSession(s.id); setMenuId(null) }}
                    >
                      <IconTrash size={14} />
                      <span>Delete</span>
                    </button>
                  </div>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
