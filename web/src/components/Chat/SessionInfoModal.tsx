import { useEffect, useState } from 'react'
import { IconX } from '@tabler/icons-react'
import * as api from '../../api'
import type { ClaudeCommand, Runner } from '../../types'
import { backdropClose } from '../../lib/backdropClose'
import { CommandDetails } from '../Runners/CommandDetails'

// "Info" of a session: the command its claude was started with, as the runner reported it.
export function SessionInfoModal({ sessionId, onClose }: { sessionId: string; onClose: () => void }) {
  const [cmd, setCmd] = useState<ClaudeCommand | null>(null)
  const [runners, setRunners] = useState<Runner[]>([])
  const [error, setError] = useState('')

  useEffect(() => {
    setCmd(null)
    setError('')
    api.getSessionCommand(sessionId).then(setCmd).catch(e => {
      const m = e instanceof Error ? e.message : String(e)
      setError(m.replace(/^.*?→ \d+: /, ''))
    })
    api.listRunners().then(setRunners).catch(() => {})
  }, [sessionId])

  const runner = cmd?.runner_id ? runners.find(r => r.id === cmd.runner_id) : undefined

  return (
    <div className="kmodal-backdrop" {...backdropClose(onClose)}>
      <div className="kmodal kmodal--wide">
        <div className="kmodal__header">
          <strong>Session info · command</strong>
          <button className="header-icon-btn" onClick={onClose} aria-label="Close"><IconX size={15} /></button>
        </div>
        <div className="kmodal__body" style={{ padding: 14, overflowY: 'auto' }}>
          {!cmd && !error && <p className="kmodal__empty-hint">Loading…</p>}
          {error && <p className="kmodal__empty-hint">{error}. It is recorded the next time this session starts claude.</p>}
          {cmd && <CommandDetails cmd={cmd} runnerName={runner ? runner.hostname : cmd.runner_id || undefined} />}
        </div>
        <div className="kmodal__footer">
          <button className="kmodal__btn kmodal__btn--cancel" onClick={onClose}>Close</button>
        </div>
      </div>
    </div>
  )
}
