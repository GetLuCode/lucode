import { useCallback, useEffect, useRef, useState } from 'react'
import { IconLayoutSidebarRightCollapse, IconLayoutSidebarRightExpand, IconSend, IconPlayerStop } from '@tabler/icons-react'
import { useStore } from '../../store'
import * as api from '../../api'
import { useSessionWS } from '../../hooks/useSessionWS'
import { MessageBubble } from '../Chat/MessageBubble'
import { ThinkingIndicator } from '../Chat/ThinkingIndicator'
import { EnvPicker } from './EnvPicker'
import type { AIProfile, Message, Runner } from '../../types'
import './OrchestratorPanel.css'

const EMPTY_MESSAGES: Message[] = []
// Anthropic's prompt cache lives 5 minutes after the last request.
const CACHE_TTL_MS = 5 * 60 * 1000
// The runner and the AI are remembered per project (and the AI per runner, since an
// AI belongs to its runner); a project without a choice starts from the last runner used.
const LS = {
  open: 'orch_open',
  runner: (pid: string) => `orch_runner:${pid}`,
  lastRunner: 'orch_runner',
  ai: (pid: string, runner: string) => `orch_ai:${pid}:${runner}`,
}

const lsGet = (k: string, def: string) => {
  try { return localStorage.getItem(k) ?? def } catch { return def }
}
const lsSet = (k: string, v: string) => {
  try { localStorage.setItem(k, v) } catch { /* private mode */ }
}

function fmtClock(ms: number) {
  const s = Math.max(0, Math.ceil(ms / 1000))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

// Counts the prompt cache down from the end of the last turn. While a turn runs the
// cache is being refreshed, so the clock is held at the full 5 minutes.
function useCacheCountdown(status: string, lastActive: string | undefined) {
  const [endsAt, setEndsAt] = useState<number | null>(null)
  const [now, setNow] = useState(() => Date.now())
  const prev = useRef(status)

  // Chat opened with an earlier conversation: its cache ends 5 min after it was last active.
  useEffect(() => {
    if (lastActive && status === 'idle') {
      const t = new Date(lastActive).getTime()
      if (!Number.isNaN(t)) setEndsAt(t + CACHE_TTL_MS)
    } else if (!lastActive) {
      setEndsAt(null)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lastActive])

  useEffect(() => {
    if (prev.current !== 'idle' && status === 'idle') setEndsAt(Date.now() + CACHE_TTL_MS)
    prev.current = status
  }, [status])

  const running = status === 'running' || status === 'waiting_hook'
  useEffect(() => {
    if (running || endsAt === null) return
    setNow(Date.now())
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [running, endsAt])

  if (running) return { label: fmtClock(CACHE_TTL_MS), state: 'busy' as const, fraction: 1 }
  if (endsAt === null) return { label: '—', state: 'none' as const, fraction: 0 }
  const left = endsAt - now
  if (left <= 0) return { label: 'cache expired', state: 'expired' as const, fraction: 0 }
  return { label: fmtClock(left), state: left < 60_000 ? ('low' as const) : ('ok' as const), fraction: left / CACHE_TTL_MS }
}

export function OrchestratorPanel() {
  const projectId = useStore(s => s.activeProjectId)
  const peersVersion = useStore(s => s.peersVersion)
  const loadMessages = useStore(s => s.loadMessages)
  const [open, setOpen] = useState(() => lsGet(LS.open, '1') === '1')
  const [session, setSession] = useState<{ id: string; last_active: string } | null>(null)
  const [error, setError] = useState('')
  const [input, setInput] = useState('')
  const [runners, setRunners] = useState<Runner[]>([])
  const [profiles, setProfiles] = useState<AIProfile[]>([])
  const [runnersLoaded, setRunnersLoaded] = useState(false)
  const [profilesLoaded, setProfilesLoaded] = useState(false)
  const [runnerId, setRunnerId] = useState('local')
  const [aiId, setAiId] = useState('')
  const scrollRef = useRef<HTMLDivElement>(null)

  const sid = session?.id ?? null
  const messages = useStore(s => (sid ? s.messages[sid] : undefined) ?? EMPTY_MESSAGES)
  const streamingText = useStore(s => (sid ? s.streamingText[sid] : '') ?? '')
  const status = useStore(s => (sid ? s.sessionStatus[sid] : undefined) ?? 'idle')
  const sessionError = useStore(s => (sid ? s.sessionError[sid] : '') ?? '')
  const sending = useStore(s => (sid ? s.pendingSend[sid] : false) ?? false)
  const { sendHookRespond } = useSessionWS(sid)

  const running = sending || status === 'running' || status === 'waiting_hook'
  const cache = useCacheCountdown(status, session?.last_active)

  const toggle = (v: boolean) => { setOpen(v); lsSet(LS.open, v ? '1' : '0') }

  // The project's orchestrator chat.
  useEffect(() => {
    if (!projectId) { setSession(null); return }
    let cancelled = false
    setSession(null)
    setError('')
    api.getOrchestrator(projectId)
      .then(async s => {
        if (cancelled) return
        await loadMessages(s.id)
        if (!cancelled) setSession({ id: s.id, last_active: s.message_count > 0 ? s.last_active : '' })
      })
      .catch(e => { if (!cancelled) setError(String(e?.message ?? e)) })
    return () => { cancelled = true }
  }, [projectId, loadMessages])

  // The saved choice of this project. Nothing below ever overwrites it by itself: a
  // runner that has not been discovered yet (or is off) must not lose the user's pick.
  useEffect(() => {
    if (!projectId) return
    setRunnerId(lsGet(LS.runner(projectId), lsGet(LS.lastRunner, 'local')))
  }, [projectId])
  useEffect(() => {
    if (!projectId) return
    setAiId(lsGet(LS.ai(projectId, runnerId), ''))
  }, [projectId, runnerId])
  const pickRunner = (v: string) => {
    setRunnerId(v)
    lsSet(LS.lastRunner, v)
    if (projectId) lsSet(LS.runner(projectId), v)
  }
  const pickAI = (id: string) => {
    setAiId(id)
    if (projectId) lsSet(LS.ai(projectId, runnerId), id)
  }

  // Runners (this one first); "this" is sent as "local".
  useEffect(() => {
    api.listRunners()
      .then(r => { setRunners(r); setRunnersLoaded(true) })
      .catch(() => { setRunners([]); setRunnersLoaded(false) })
  }, [peersVersion])
  // this machine is shown by its own name
  const selfName = runners.find(r => r.self)?.hostname || 'this'
  const runnerPath = runnerId
  // A saved remote runner that is not (yet) on the network: say so and do not send.
  const runnerOffline = runnerId !== 'local' && runnersLoaded && !runners.some(r => r.id === runnerId)
  const runnerName = runnerId === 'local' ? selfName : runners.find(r => r.id === runnerId)?.hostname || 'offline runner'

  // Preconfigured AIs of the selected runner.
  useEffect(() => {
    if (runnerOffline) { setProfiles([]); return }
    let cancelled = false
    setProfiles([])
    setProfilesLoaded(false)
    api.listAIProfiles(runnerPath)
      .then(p => { if (!cancelled) { setProfiles(p ?? []); setProfilesLoaded(true) } })
      .catch(() => { if (!cancelled) setProfiles([]) })
    return () => { cancelled = true }
  }, [runnerPath, runnerOffline, peersVersion])
  // A saved AI that was deleted behaves as "default" (the saved id stays until the user picks another).
  const effectiveAi = aiId && profilesLoaded && !profiles.some(p => p.id === aiId) ? '' : aiId

  // Keep the newest message in view.
  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [messages.length, streamingText, open])

  const send = useCallback(() => {
    const text = input.trim()
    if (!text || !sid || running || runnerOffline) return
    setInput('')
    useStore.setState(s => ({ pendingSend: { ...s.pendingSend, [sid]: true } }))
    api.sendOrchestratorTurn(sid, text, { runnerId, aiProfileId: effectiveAi }).catch(e => {
      useStore.setState(s => {
        const next = { ...s.pendingSend }
        delete next[sid]
        return { pendingSend: next, sessionError: { ...s.sessionError, [sid]: String(e?.message ?? e) } }
      })
    })
  }, [input, sid, running, runnerId, effectiveAi, runnerOffline])

  const cancel = () => { if (sid) api.cancelTurn(sid).catch(console.error) }

  if (!projectId) return null

  if (!open) {
    return (
      <button className="orch-tab" onClick={() => toggle(true)} title="Open the orchestrator chat">
        <IconLayoutSidebarRightExpand size={16} />
        <span>Orchestrator</span>
        <CacheBadge cache={cache} compact />
      </button>
    )
  }

  return (
    <aside className="orch">
      <header className="orch__header">
        <span className="orch__title">Orchestrator</span>
        <CacheBadge cache={cache} />
        <button className="orch__icon-btn" onClick={() => toggle(false)} title="Hide">
          <IconLayoutSidebarRightCollapse size={16} />
        </button>
      </header>

      <div className="orch__messages" ref={scrollRef}>
        {error && <div className="orch__error">{error}</div>}
        {!error && messages.length === 0 && !running && (
          <div className="orch__empty">Tell me what you want done. I plan it on the board and hand it to the agents.</div>
        )}
        {messages.map(m => (
          <div key={m.id} className="orch__msg">
            <MessageBubble message={m} sendHookRespond={sendHookRespond} />
          </div>
        ))}
        {streamingText && (
          <div className="msg msg--assistant">
            <div className="msg__bubble"><p className="msg__text msg__text--streaming">{streamingText}</p></div>
          </div>
        )}
        {running && !streamingText && <ThinkingIndicator />}
        {status === 'error' && sessionError && <div className="orch__error">{sessionError}</div>}
      </div>

      <div className="orch__input">
        <textarea
          className="orch__textarea"
          rows={3}
          value={input}
          placeholder={sid ? 'Message the orchestrator…' : 'Loading…'}
          disabled={!sid}
          onChange={e => setInput(e.target.value)}
          onKeyDown={e => {
            if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() }
            else if (e.key === 'Escape' && running) { e.preventDefault(); cancel() }
          }}
        />
        <div className="orch__controls">
          {/* "default" stands for no preconfigured AI (stored as an empty id) */}
          <EnvPicker
            runner={runnerId}
            runners={[
              { value: 'local', label: selfName },
              ...runners.filter(r => !r.self).map(r => ({ value: r.id, label: r.hostname || r.id })),
              ...(runnerOffline ? [{ value: runnerId, label: `${runnerName} (offline)` }] : []),
            ]}
            onRunner={pickRunner}
            ai={effectiveAi || 'default'}
            profiles={[
              { value: 'default', label: 'default' },
              ...profiles.map(p => ({ value: p.id, label: p.name, hint: p.email || undefined })),
            ]}
            onAI={v => pickAI(v === 'default' ? '' : v)}
          />
          <span className="orch__env-summary" title="Runner · AI">
            {runnerName}{runnerOffline ? ' (offline)' : ''} · {profiles.find(p => p.id === effectiveAi)?.name ?? 'default'}
          </span>
          {running ? (
            <button className="orch__send orch__send--stop" onClick={cancel} title="Stop (Esc)"><IconPlayerStop size={15} /></button>
          ) : (
            <button className="orch__send" onClick={send} disabled={!input.trim() || !sid || runnerOffline} title={runnerOffline ? 'The selected runner is offline' : 'Send (Enter)'}><IconSend size={15} /></button>
          )}
        </div>
      </div>
    </aside>
  )
}

function CacheBadge({ cache, compact }: { cache: ReturnType<typeof useCacheCountdown>; compact?: boolean }) {
  const title = 'Prompt cache: time left before the 5-minute cache expires. A new message within it is cheaper and faster.'
  return (
    <span className={`orch-cache orch-cache--${cache.state}${compact ? ' orch-cache--compact' : ''}`} title={title}>
      <span className="orch-cache__bar"><span style={{ transform: `scaleX(${cache.fraction.toFixed(3)})` }} /></span>
      <span className="orch-cache__time">{cache.label}</span>
    </span>
  )
}
