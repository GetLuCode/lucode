import { useCallback, useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { IconLayoutSidebarRightCollapse, IconLayoutSidebarRightExpand, IconSend, IconPlayerStop, IconMicrophone, IconVolume, IconVolumeOff, IconSettings, IconMessagePlus } from '@tabler/icons-react'
import { useStore } from '../../store'
import * as api from '../../api'
import { useSessionWS } from '../../hooks/useSessionWS'
import { MessageBubble } from '../Chat/MessageBubble'
import { ThinkingIndicator } from '../Chat/ThinkingIndicator'
import { ContextBar } from '../Chat/ContextBar'
import { CacheBadge, useCacheCountdown } from '../Chat/CacheBadge'
import { ClaudeProcess } from '../Chat/ClaudeProcess'
import { AIModeHistory } from './AIModeHistory'
import { ModelIcon } from './ModelIcon'
import { EnvPicker } from './EnvPicker'
import { useWorkerMic } from '@/hooks/useWorkerMic'
import { useWorkerTTS } from '@/hooks/useWorkerTTS'
import { markSpoken, wasSpoken } from '@/lib/spokenLog'
import { VoiceSettingsDialog } from './VoiceSettingsDialog'
import { getAutoSend, getHotword } from '@/lib/audioDevices'
import type { AIProfile, Message, Runner, Session } from '../../types'
import './OrchestratorPanel.css'

const EMPTY_MESSAGES: Message[] = []
// The runner and the AI are remembered per project (and the AI per runner, since an
// AI belongs to its runner); a project without a choice starts from the last runner used.
const LS = {
  autoRead: 'orch_autoread',
  runner: (pid: string) => `orch_runner:${pid}`,
  lastRunner: 'orch_runner',
  ai: (pid: string, runner: string) => `orch_ai:${pid}:${runner}`,
  model: (pid: string) => `orch_model:${pid}`,
}

const MODELS = [
  { value: 'haiku', label: 'Haiku 5.5' },
  { value: 'sonnet', label: 'Sonnet 5.5' },
  { value: 'opus', label: 'Opus 5.5' },
  // Local: Google's AI Mode in an Edge that lucode drives. No claude, no tools, no tokens.
  { value: 'local:aimode', label: 'Google AI Mode (Edge)' },
]

const lsGet = (k: string, def: string) => {
  try { return localStorage.getItem(k) ?? def } catch { return def }
}
const lsSet = (k: string, v: string) => {
  try { localStorage.setItem(k, v) } catch { /* private mode */ }
}

// The context window of an earlier conversation, as last measured, so the bar is there on load.
function seedContextUsage(s: Session) {
  if (!s.context_usage) return
  try {
    const cu = JSON.parse(s.context_usage)
    if (cu && cu.totalTokens != null) {
      useStore.setState(st => ({ contextUsage: { ...st.contextUsage, [s.id]: { ...cu, session_id: s.id } } }))
    }
  } catch { /* malformed: the next turn measures it again */ }
}

// `full`: the Agent tab, where the panel fills the page whether or not it is collapsed as a side panel.
export function OrchestratorPanel({ full = false }: { full?: boolean }) {
  const projectId = useStore(s => s.activeProjectId)
  const peersVersion = useStore(s => s.peersVersion)
  const loadMessages = useStore(s => s.loadMessages)
  const storedOpen = useStore(s => s.orchOpen)
  const open = full || storedOpen
  const setOpen = useStore(s => s.setOrchOpen)
  const setOrchCache = useStore(s => s.setOrchCache)
  const onKanban = useLocation().pathname.includes('/kanban')
  const navigate = useNavigate()
  const [session, setSession] = useState<{ id: string; last_active: string } | null>(null)
  const [error, setError] = useState('')
  const [input, setInput] = useState('')
  // Dictation: what was typed before is kept; the phrase being spoken follows it.
  const dictBase = useRef('')
  const joinText = (a: string, b: string) => (a && b ? `${a.replace(/\s+$/, '')} ${b}` : a || b)
  const mic = useWorkerMic({
    onRealtime: text => setInput(joinText(dictBase.current, text)),
    onFinal: (text, viaHotword) => {
      if (!text.trim()) return
      // a command said after the hotword is sent on its own; if the agent is busy it waits in the box
      if (viaHotword && !runningRef.current) { sendText(text.trim()); return }
      dictBase.current = joinText(dictBase.current, text)
      setInput(dictBase.current)
      // plain dictation with "auto-send" on: the finished phrase turns the mic off and goes out
      // (if the agent is busy it stays in the box, mic off, to be sent by hand)
      if (!viaHotword && getAutoSend()) {
        mic.stop()
        if (!runningRef.current) sendText(dictBase.current.trim())
      }
    },
  })
  // Voice output: a button per answer, plus an auto-read switch (off by default: Fish bills by text).
  const tts = useWorkerTTS()
  const [voiceOpen, setVoiceOpen] = useState(false)
  const [speakingId, setSpeakingId] = useState<string | null>(null)
  const [ttsConfigured, setTtsConfigured] = useState(true) // unknown until the dialog or a speak says otherwise
  const [autoRead, setAutoRead] = useState(() => lsGet(LS.autoRead, '0') === '1')
  const speakMessage = (id: string, content: string) => {
    if (!ttsConfigured) { setVoiceOpen(true); return }
    setSpeakingId(id)
    markSpoken(sid ?? '', content)
    void tts.speak(content)
  }
  useEffect(() => { api.getTTSSettings().then(s => setTtsConfigured(s.configured)).catch(() => {}) }, [])
  useEffect(() => { if (tts.state === 'idle') setSpeakingId(null) }, [tts.state])
  // the dictation must not hear the answer being spoken
  useEffect(() => { mic.setMuted(tts.state !== 'idle') }, [tts.state, mic])
  const hotword = getHotword()
  const micTitle = {
    off: hotword.enabled ? `Listen for the hotword${hotword.backend === 'vosk' ? ` “${hotword.phrase}”` : ' “hey jarvis”'}` : 'Dictate (Parakeet, local)',
    loading: 'Loading the voice model…',
    listening: 'Stop dictating',
    armed: 'Waiting for the hotword (click to stop)',
    awake: 'Listening for your command…',
  }[mic.state]
  const toggleMic = () => {
    if (mic.state === 'off') { dictBase.current = input; void mic.start() } else mic.stop()
  }
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
  const liveMessageId = useStore(s => (sid ? s.liveMessageId[sid] : undefined))

  // Auto-read: only an answer that arrives live, never the history when the chat opens.
  useEffect(() => {
    if (!autoRead || !liveMessageId) return
    const m = useStore.getState().messages[sid ?? '']?.find(x => x.id === liveMessageId)
    if (!m || m.role !== 'assistant' || !ttsConfigured) return
    // a reload can re-deliver the last answer as live: never read the same text twice on its own
    if (wasSpoken(sid ?? '', m.content)) return
    markSpoken(sid ?? '', m.content)
    speakMessage(m.id, m.content)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [liveMessageId])
  const { sendHookRespond } = useSessionWS(sid)

  const running = sending || status === 'running' || status === 'waiting_hook'
  const cache = useCacheCountdown(status, session?.last_active, sid ?? '')
  // the button that reopens the panel (wherever it is drawn) shows this clock
  useEffect(() => { setOrchCache(cache) }, [cache, setOrchCache])

  const toggle = (v: boolean) => setOpen(v)

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
        seedContextUsage(s)
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
  const [model, setModel] = useState('sonnet')
  useEffect(() => {
    if (!projectId) return
    const saved = lsGet(LS.model(projectId), 'sonnet')
    setModel(MODELS.some(m => m.value === saved) ? saved : 'sonnet')
  }, [projectId])
  const setOrchModel = useStore(s => s.setOrchModel)
  useEffect(() => { setOrchModel(model) }, [model, setOrchModel]) // the buttons that reopen the panel show its mark
  const isLocalModel = model.startsWith('local:') // answered by a browser: no cache, context or process to show
  const pickModel = (v: string) => {
    setModel(v)
    if (projectId) lsSet(LS.model(projectId), v)
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

  const runningRef = useRef(false)
  runningRef.current = running
  const sendText = (text: string) => {
    if (!text || !sid || running || (runnerOffline && !isLocalModel)) return
    setInput('')
    dictBase.current = ''
    useStore.setState(s => ({ pendingSend: { ...s.pendingSend, [sid]: true } }))
    api.sendOrchestratorTurn(sid, text, { runnerId, aiProfileId: effectiveAi, model }).catch(e => {
      useStore.setState(s => {
        const next = { ...s.pendingSend }
        delete next[sid]
        return { pendingSend: next, sessionError: { ...s.sessionError, [sid]: String(e?.message ?? e) } }
      })
    })
  }
  const send = () => sendText(input.trim())

  const cancel = () => { if (sid) api.cancelTurn(sid).catch(console.error) }

  // A Google AI Mode conversation picked from the history: its chat comes back with its
  // messages (or, if it was an ordinary chat, it is opened); the next message goes to the same page.
  const continueConversation = async (s: Session) => {
    if (s.kind !== 'orchestrator') { navigate(`/projects/${s.project_id}/sessions/${s.id}`); return }
    tts.stop()
    setError('')
    try {
      await loadMessages(s.id)
      seedContextUsage(s)
      setInput('')
      dictBase.current = ''
      setSession({ id: s.id, last_active: s.message_count > 0 ? s.last_active : '' })
    } catch (e) {
      setError(String((e as Error)?.message ?? e))
    }
  }

  // A fresh conversation with an empty context window; the old one is closed.
  const startNewSession = async () => {
    if (!projectId || running) return
    if (messages.length > 0 && !confirm('Start a new orchestrator session?\n\nThe current conversation is closed and its context is cleared.')) return
    tts.stop()
    setError('')
    try {
      const s = await api.newOrchestratorSession(projectId)
      await loadMessages(s.id)
      setInput('')
      dictBase.current = ''
      setSession({ id: s.id, last_active: '' })
    } catch (e) {
      setError(String((e as Error)?.message ?? e))
    }
  }

  if (!projectId) return null

  if (!open) {
    if (onKanban) return null // the board has its own button, after "Add list"
    return (
      <button className="orch-tab" onClick={() => toggle(true)} title="Open the orchestrator chat">
        <ModelIcon model={model} size={16} />
        <IconLayoutSidebarRightExpand size={16} />
        <span>Orchestrator</span>
        {!isLocalModel && <CacheBadge cache={cache} compact />}
      </button>
    )
  }

  return (
    <aside className={`orch${full ? ' orch--full' : ''}`}>
      <header className="orch__header">
        <ModelIcon model={model} size={17} className="orch__model-icon" />
        <span className="orch__title">Orchestrator</span>
        {!isLocalModel && <CacheBadge cache={cache} />}
        {sid && !isLocalModel && <ClaudeProcess sessionId={sid} />}
        {isLocalModel && projectId && <AIModeHistory projectId={projectId} onContinue={continueConversation} />}
        <button
          className="orch__icon-btn"
          onClick={startNewSession}
          disabled={!sid || running}
          title={running ? 'Stop the current turn before starting a new session' : 'New session (empty context)'}
          aria-label="New session"
        >
          <IconMessagePlus size={16} />
        </button>
        <button
          className={`orch__icon-btn${autoRead ? ' orch__icon-btn--on' : ''}`}
          onClick={() => { const v = !autoRead; setAutoRead(v); lsSet(LS.autoRead, v ? '1' : '0'); if (!v) tts.stop(); if (v && !ttsConfigured) setVoiceOpen(true) }}
          title={autoRead ? 'Reading answers aloud (click to stop)' : 'Read answers aloud automatically'}
          aria-pressed={autoRead}
        >
          {autoRead ? <IconVolume size={16} /> : <IconVolumeOff size={16} />}
        </button>
        <button className="orch__icon-btn" onClick={() => setVoiceOpen(true)} title="Voice settings">
          <IconSettings size={16} />
        </button>
        {!full && (
          <button className="orch__icon-btn" onClick={() => toggle(false)} title="Hide">
            <IconLayoutSidebarRightCollapse size={16} />
          </button>
        )}
      </header>

      {sid && !isLocalModel && <ContextBar sessionId={sid} placement="panel" />}

      <div className="orch__messages" ref={scrollRef}>
        {error && <div className="orch__error">{error}</div>}
        {!error && messages.length === 0 && !running && (
          <div className="orch__empty">Tell me what you want done. I plan it on the board and hand it to the agents.</div>
        )}
        {messages.map(m => (
          <div key={m.id} className="orch__msg">
            <MessageBubble
              message={m}
              sendHookRespond={sendHookRespond}
              isBeingRead={speakingId === m.id}
              onSpeak={speakMessage}
              onStopSpeaking={tts.stop}
            />
          </div>
        ))}
        {streamingText && (
          <div className="msg msg--assistant">
            <div className="msg__bubble"><p className="msg__text msg__text--streaming">{streamingText}</p></div>
          </div>
        )}
        {running && !streamingText && <ThinkingIndicator sessionId={sid} model={model} />}
        {tts.error && <div className="orch__error">{tts.error}</div>}
        {status === 'error' && sessionError && <div className="orch__error">{sessionError}</div>}
      </div>

      <div className="orch__input">
        <textarea
          className="orch__textarea"
          rows={3}
          value={input}
          placeholder={sid ? 'Message the orchestrator…' : 'Loading…'}
          disabled={!sid}
          onChange={e => {
            setInput(e.target.value)
            // typing while dictating: the dictation continues after what is now in the box
            if (mic.state !== 'off') dictBase.current = e.target.value
          }}
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
            model={model}
            models={MODELS}
            onModel={pickModel}
          />
          <span className="orch__env-summary" title="Runner · AI · Model">
            {runnerName}{runnerOffline ? ' (offline)' : ''} · {profiles.find(p => p.id === effectiveAi)?.name ?? 'default'} · {MODELS.find(m => m.value === model)?.label ?? model}
          </span>
          <button
            className={`orch__mic orch__mic--${mic.state}`}
            onClick={toggleMic}
            title={mic.error || micTitle}
            aria-pressed={mic.state !== 'off'}
          >
            <IconMicrophone size={15} />
          </button>
          {running ? (
            <button className="orch__send orch__send--stop" onClick={cancel} title="Stop (Esc)"><IconPlayerStop size={15} /></button>
          ) : (
            <button className="orch__send" onClick={send} disabled={!input.trim() || !sid || (runnerOffline && !isLocalModel)} title={runnerOffline && !isLocalModel ? 'The selected runner is offline' : 'Send (Enter)'}><IconSend size={15} /></button>
          )}
        </div>
      </div>
      {voiceOpen && <VoiceSettingsDialog onClose={() => setVoiceOpen(false)} onSaved={setTtsConfigured} />}
    </aside>
  )
}

