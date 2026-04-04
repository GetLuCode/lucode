import { useState } from 'react'
import { useStore } from '../../store'
import type { Session } from '../../types'

export function SessionList() {
  const sessions = useStore(s => s.sessions)
  const activeSessionId = useStore(s => s.activeSessionId)
  const activeProjectId = useStore(s => s.activeProjectId)
  const setActiveSession = useStore(s => s.setActiveSession)
  const createSession = useStore(s => s.createSession)
  const deleteSession = useStore(s => s.deleteSession)
  const sessionStatus = useStore(s => s.sessionStatus)
  const [showNew, setShowNew] = useState(false)
  const [name, setName] = useState('')

  if (!activeProjectId) {
    return (
      <div className="session-list session-list--empty">
        <span>Select a project</span>
      </div>
    )
  }

  const handleCreate = async () => {
    if (!activeProjectId) return
    const sess = await createSession(activeProjectId, { name: name.trim() || 'New session' })
    await setActiveSession(sess.id)
    setShowNew(false)
    setName('')
  }

  const statusDot = (sess: Session) => {
    const s = sessionStatus[sess.id] ?? sess.status
    if (s === 'running') return '●'
    if (s === 'waiting_hook') return '◆'
    if (s === 'error') return '✕'
    return '○'
  }

  const statusClass = (sess: Session) => {
    const s = sessionStatus[sess.id] ?? sess.status
    return `status-dot status-dot--${s}`
  }

  return (
    <div className="session-list">
      <div className="session-list__header">
        <span className="session-list__title">Sessions</span>
        <button className="icon-btn" onClick={() => setShowNew(v => !v)} title="New session">+</button>
      </div>

      {showNew && (
        <div className="session-list__new">
          <input
            className="input"
            placeholder="Session name"
            value={name}
            onChange={e => setName(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && handleCreate()}
            autoFocus
          />
          <button className="btn btn--primary" onClick={handleCreate}>Create</button>
        </div>
      )}

      <ul className="session-list__items">
        {sessions.map((s: Session) => (
          <li
            key={s.id}
            className={`session-list__item${activeSessionId === s.id ? ' session-list__item--active' : ''}`}
            onClick={() => setActiveSession(s.id)}
          >
            <span className={statusClass(s)}>{statusDot(s)}</span>
            <div className="session-list__info">
              <span className="session-list__name">{s.name || 'Untitled'}</span>
              <span className="session-list__meta">{s.message_count} msgs</span>
            </div>
            <button
              className="icon-btn icon-btn--danger"
              onClick={e => { e.stopPropagation(); deleteSession(s.id) }}
              title="Delete"
            >×</button>
          </li>
        ))}
      </ul>
    </div>
  )
}
