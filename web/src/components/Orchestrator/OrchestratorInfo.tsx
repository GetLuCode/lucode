import { useEffect, useState } from 'react'
import { IconInfoCircle, IconPlugConnectedX } from '@tabler/icons-react'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { useStore } from '../../store'
import * as api from '../../api'
import type { CacheCountdown } from '../Chat/CacheBadge'

// What the header of the narrow panel leaves out: the state of the prompt cache and of the claude
// process (with the full runner id), and the action to close that process.
export function OrchestratorInfo({ sessionId, cache }: { sessionId: string; cache: CacheCountdown }) {
  const info = useStore(s => s.claudeProcess[sessionId])
  const status = useStore(s => s.sessionStatus[sessionId] ?? 'idle')
  const [open, setOpen] = useState(false)
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
  const ws = info?.workspace
  const cacheText = {
    busy: 'Refreshing (a turn is running)',
    none: 'No conversation yet',
    expired: 'Expired: the next message pays for the whole context again',
    low: `${cache.label} left`,
    ok: `${cache.label} left`,
  }[cache.state]
  const processText = alive
    ? `Alive${info?.runner ? ` on runner ${info.runner}` : info?.pid ? ` (pid ${info.pid})` : ''}`
    : turnRunning ? 'Running this turn' : 'None (the next message starts one)'

  const close = async () => {
    setError('')
    try {
      await api.closeSessionProcess(sessionId)
      setOpen(false)
    } catch (e) {
      setError(String((e as Error)?.message ?? e))
    }
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button className={`orch__icon-btn${cache.state === 'expired' ? ' orch__icon-btn--warn' : ''}`} title="Cache and claude process" aria-label="Cache and claude process">
          <IconInfoCircle size={16} />
        </button>
      </PopoverTrigger>
      <PopoverContent side="bottom" align="end" className="env-picker orch-info">
        <div className="env-picker__title">Prompt cache</div>
        <div className={`orch-info__row orch-info__row--${cache.state}`}>{cacheText}</div>
        <div className="env-picker__divider" />
        <div className="env-picker__title">claude process</div>
        <div className="orch-info__row">{processText}</div>
        {ws?.branch && <div className="orch-info__row">{ws.worktree ? 'worktree ' : 'branch '}{ws.branch}</div>}
        {ws?.cwd && <div className="orch-info__row orch-info__row--path" title={ws.cwd}>{ws.cwd}</div>}
        {alive && (
          <>
            <div className="env-picker__divider" />
            <button
              type="button"
              className="env-picker__item orch-info__close"
              onClick={close}
              disabled={turnRunning}
              title={turnRunning ? 'A turn is running: stop it first' : undefined}
            >
              <IconPlugConnectedX size={14} />
              <span className="env-picker__item-text">Close claude process</span>
            </button>
            <div className="env-picker__hint orch-info__hint">The next message starts a new one and resumes the conversation.</div>
          </>
        )}
        {error && <div className="orch-info__error">{error}</div>}
      </PopoverContent>
    </Popover>
  )
}
