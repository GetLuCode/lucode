import { useRef, useState } from 'react'
import { useStore } from '../../store'
import * as api from '../../api'
import type { Session } from '../../types'

export function SessionList() {
  const sessions = useStore(s => s.sessions)
  const activeSessionId = useStore(s => s.activeSessionId)
  const activeProjectId = useStore(s => s.activeProjectId)
  const setActiveSession = useStore(s => s.setActiveSession)
  const setPendingSession = useStore(s => s.setPendingSession)
  const deleteSession = useStore(s => s.deleteSession)
  const renameSession = useStore(s => s.renameSession)
  const importSession = useStore(s => s.importSession)
  const sessionStatus = useStore(s => s.sessionStatus)
  const projects = useStore(s => s.projects)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editingName, setEditingName] = useState('')
  const importRef = useRef<HTMLInputElement>(null)

  if (!activeProjectId) {
    return (
      <div className="session-list session-list--empty">
        <span>Select a project</span>
      </div>
    )
  }

  const handleCreateNew = () => {
    const proj = projects.find(p => p.id === activeProjectId)
    const model = proj?.default_model || 'haiku'
    setPendingSession(activeProjectId, model)
  }

  const handleExport = async (e: React.MouseEvent, sess: Session) => {
    e.stopPropagation()
    try {
      const blob = await api.exportSession(sess.id)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `${(sess.name || 'session').replace(/[^a-zA-Z0-9_-]/g, '_')}.json`
      a.click()
      URL.revokeObjectURL(url)
    } catch (err) {
      console.error('export failed', err)
    }
  }

  const handleImportClick = () => importRef.current?.click()

  const handleImportFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    if (!file || !activeProjectId) return
    e.target.value = ''
    try {
      const text = await file.text()
      const data = JSON.parse(text)
      await importSession(activeProjectId, data)
    } catch (err) {
      console.error('import failed', err)
    }
  }

  const handleStartEdit = (sess: Session) => {
    setEditingId(sess.id)
    setEditingName(sess.name)
  }

  const handleSaveEdit = async () => {
    if (editingId && editingName.trim()) {
      await renameSession(editingId, editingName.trim())
    }
    setEditingId(null)
    setEditingName('')
  }

  const handleCancelEdit = () => {
    setEditingId(null)
    setEditingName('')
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
        <button className="icon-btn" onClick={handleImportClick} title="Import session">↑</button>
        <button className="icon-btn" onClick={handleCreateNew} title="New session">+</button>
        <input ref={importRef} type="file" accept=".json" style={{ display: 'none' }} onChange={handleImportFile} />
      </div>

      <ul className="session-list__items">
        {sessions.map((s: Session) => (
          <li
            key={s.id}
            className={`session-list__item${activeSessionId === s.id ? ' session-list__item--active' : ''}`}
            onClick={() => setActiveSession(s.id)}
          >
            <span className={statusClass(s)}>{statusDot(s)}</span>
            <div className="session-list__info">
              {editingId === s.id ? (
                <input
                  className="input session-list__edit-input"
                  value={editingName}
                  onChange={e => setEditingName(e.target.value)}
                  onKeyDown={e => {
                    e.stopPropagation()
                    if (e.key === 'Enter') handleSaveEdit()
                    if (e.key === 'Escape') handleCancelEdit()
                  }}
                  onBlur={handleSaveEdit}
                  onClick={e => e.stopPropagation()}
                  autoFocus
                />
              ) : (
                <>
                  <span
                    className="session-list__name"
                    onDoubleClick={(e) => {
                      e.stopPropagation()
                      handleStartEdit(s)
                    }}
                  >
                    {s.name || 'Untitled'}
                  </span>
                  <span className="session-list__meta">{s.message_count} msgs</span>
                </>
              )}
            </div>
            <button
              className="icon-btn"
              onClick={e => handleExport(e, s)}
              title="Export session"
            >↓</button>
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
