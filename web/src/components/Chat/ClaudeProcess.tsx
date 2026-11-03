import { useEffect, useState } from 'react'
import { IconDots, IconPlugConnectedX } from '@tabler/icons-react'
import { useStore } from '../../store'
import * as api from '../../api'
import './ClaudeProcess.css'

// Shows whether the session has a claude process, next to its status, and a three-dot
// menu to close it. A persistent process stays alive between turns (until idle); closing
// it frees its memory, and the next message starts a new one that resumes the conversation.
export function ClaudeProcess({ sessionId }: { sessionId: string }) {
  const info = useStore(s => s.claudeProcess[sessionId])
  const status = useStore(s => s.sessionStatus[sessionId] ?? 'idle')
  const [menuOpen, setMenuOpen] = useState(false)
  const [error, setError] = useState('')

  // The current state; after that the websocket keeps it up to date.
  useEffect(() => {
    let cancelled = false
    api.getSessionProcess(sessionId)
      .then(p => {
        if (cancelled) return
        useStore.setState(s => (s.claudeProcess[sessionId] ? s : { claudeProcess: { ...s.claudeProcess, [sessionId]: p } }))
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [sessionId])

  const turnRunning = status === 'running' || status === 'waiting_hook'
  const alive = !!info?.alive
  const where = info?.runner ? ` @${info.runner}` : info?.pid ? ` ${info.pid}` : ''
  const state = alive ? 'alive' : 'turn'
  const label = alive ? `claude${where}` : 'claude'
  const title = alive
    ? `claude process alive${info?.runner ? ` on the runner ${info.runner}` : info?.pid ? ` (pid ${info.pid})` : ''}`
    : 'claude is running this turn'

  const close = async () => {
    setMenuOpen(false)
    setError('')
    try {
      await api.closeSessionProcess(sessionId)
    } catch (e) {
      setError(String((e as Error)?.message ?? e))
    }
  }

  // No claude in this session: show nothing.
  if (!alive && !turnRunning) return null

  return (
    <div className="claude-proc header-menu">
      <span className={`claude-proc__badge claude-proc__badge--${state}`} title={error || title}>
        <span className="claude-proc__dot" />
        {label}
      </span>
      {alive && (
        <button
          className="claude-proc__menu-btn"
          onClick={() => setMenuOpen(v => !v)}
          title="claude process"
          aria-label="claude process menu"
          aria-haspopup="menu"
          aria-expanded={menuOpen}
        >
          <IconDots size={14} />
        </button>
      )}
      {alive && menuOpen && (
        <>
          <div className="header-menu__backdrop" onClick={() => setMenuOpen(false)} />
          <div className="nav-tree__dropdown header-menu__dropdown" role="menu">
            <button
              className="nav-tree__menu-item nav-tree__menu-item--danger"
              role="menuitem"
              onClick={close}
              disabled={turnRunning}
              title={turnRunning ? 'A turn is running: stop it first' : undefined}
            >
              <IconPlugConnectedX size={14} /><span>Close claude process</span>
            </button>
            <div className="claude-proc__hint">The next message starts a new one and resumes the conversation.</div>
          </div>
        </>
      )}
    </div>
  )
}
