import { Fragment, useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { IconChevronDown, IconCheck, IconLayoutSidebarRightCollapse,IconSend, IconPlayerStop, IconMicrophone, IconVolume, IconVolumeOff, IconSettings, IconMessagePlus, IconPaperclip, IconX, IconCode, IconHistory, IconInfoCircle } from '@tabler/icons-react'
import { useStore } from '../../store'
import { usePlugins } from '../../store/plugins'
import { conversationsOfModel, isPluginModel, pluginOriginLabel, normalizeModelId, parsePluginModel } from '../../lib/plugins'
import * as api from '../../api'
import { useSessionWS } from '../../hooks/useSessionWS'
import { MessageBubble } from '../Chat/MessageBubble'
import { ThinkingIndicator } from '../Chat/ThinkingIndicator'
import { ContextBar } from '../Chat/ContextBar'
import { CacheBadge, useCacheCountdown } from '../Chat/CacheBadge'
import { ClaudeProcess } from '../Chat/ClaudeProcess'
import { HistoryPopover, HistorySidebar, latestConversation, type HistorySource } from './ConversationHistory'
import { OrchestratorEdit } from './OrchestratorEdit'
import { ModelIcon } from './ModelIcon'
import { PluginOrigin } from '../Plugins/PluginIcon'
import { EnvPicker } from './EnvPicker'
import { useMic } from '@/hooks/useMic'
import { useIsMobile } from '@/hooks/useIsMobile'
import { playListenCue, playSendCue } from '@/lib/cues'
import { useWorkerTTS } from '@/hooks/useWorkerTTS'
import { markSpoken, wasSpoken } from '@/lib/spokenLog'
import { VoiceSettingsDialog } from './VoiceSettingsDialog'
import { getAutoSend, getHotword, getSttEngine } from '@/lib/audioDevices'
import type { AIProfile, Message, PluginConversationContribution, Runner, Session } from '../../types'
import './OrchestratorPanel.css'

const EMPTY_MESSAGES: Message[] = []
// The runner and the AI are remembered per project (and the AI per runner, since an
// AI belongs to its runner); a project without a choice starts from the last runner used.
const LS = {
  autoRead: 'orch_autoread',
  historyOpen: 'orch_history_open',
  runner: (pid: string) => `orch_runner:${pid}`,
  lastRunner: 'orch_runner',
  ai: (pid: string, runner: string) => `orch_ai:${pid}:${runner}`,
  model: (pid: string) => `orch_model:${pid}`,
}

const CLAUDE_MODELS = [
  { value: 'haiku', label: 'Haiku 5.5' },
  { value: 'sonnet', label: 'Sonnet 5.5' },
  { value: 'opus', label: 'Opus 5.5' },
]
// The modes are the Supervisor plus whatever models the enabled plugins contribute. A plugin's
// model answers on the hub: no runner, no model choice, no claude, no tools, no tokens.
const SUPERVISOR_MODE = { value: 'supervisor', label: 'Supervisor', hint: 'Claude, on a runner' }
const panelCanHold = (kind?: string) => kind === 'orchestrator' || kind === 'plugin'

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

// A conversation inside a plugin's page: the model is fixed, and its chats come from the plugin.
export interface EmbeddedConversation {
  pluginId: string
  model: string
  label: string
  source: PluginConversationContribution
  rowId: string // a saved conversation to open, when the page names one
}

async function openEmbedded(projectId: string, embed: EmbeddedConversation): Promise<Session> {
  const rows = await api.listPluginConversations(embed.pluginId, projectId, embed.source.id)
  const row = embed.rowId ? rows.find(r => r.id === embed.rowId) : rows.find(r => r.session_exists)
  return row
    ? api.continuePluginConversation(embed.pluginId, row.id, 'page')
    : api.createPluginConversation(embed.pluginId, projectId, embed.model)
}

// `full`: the Agent tab, where the panel fills the page whether or not it is collapsed as a side panel.
// `embed`: inside a plugin's page, which it fills, with its own chats and no share in the side panel's state.
export function OrchestratorPanel({ full = false, embed }: { full?: boolean; embed?: EmbeddedConversation }) {
  const projectId = useStore(s => s.activeProjectId)
  const peersVersion = useStore(s => s.peersVersion)
  const loadMessages = useStore(s => s.loadMessages)
  const storedOpen = useStore(s => s.orchOpen)
  const open = full || !!embed || storedOpen
  const setOpen = useStore(s => s.setOrchOpen)
  const setOrchCache = useStore(s => s.setOrchCache)
  const navigate = useNavigate()
  const [session, setSession] = useState<{ id: string; last_active: string } | null>(null)
  const [error, setError] = useState('')
  // an image waiting to go with the next message (claude takes it as a block; Google AI Mode gets it uploaded)
  const [image, setImage] = useState<{ base64: string; mediaType: string } | null>(null)
  const fileRef = useRef<HTMLInputElement>(null)
  const [input, setInput] = useState('')
  // References sent from other tabs wait here for the question that goes with them.
  const allRefs = useStore(s => s.orchRefs)
  const refs = embed ? [] : allRefs.filter(r => r.projectId === projectId)
  const removeRef = useStore(s => s.removeOrchRef)
  const clearRefs = useStore(s => s.clearOrchRefs)
  const textareaRef = useRef<HTMLTextAreaElement>(null)
  useEffect(() => { if (refs.length > 0) textareaRef.current?.focus() }, [refs.length, storedOpen])
  // Dictation: what was typed before is kept; the phrase being spoken follows it.
  const dictBase = useRef('')
  const joinText = (a: string, b: string) => (a && b ? `${a.replace(/\s+$/, '')} ${b}` : a || b)
  const mic = useMic({
    onRealtime: text => setInput(joinText(dictBase.current, text)),
    onFinal: (text, viaHotword) => {
      if (!text.trim()) return
      // a command said after the hotword is sent on its own; if the agent is busy it waits in the box
      if (viaHotword && !runningRef.current) { playSendCue(); sendText(text.trim()); return }
      dictBase.current = joinText(dictBase.current, text)
      setInput(dictBase.current)
      // plain dictation with "auto-send" on: the finished phrase turns the mic off and goes out
      // (if the agent is busy it stays in the box, mic off, to be sent by hand)
      if (!viaHotword && getAutoSend()) {
        mic.stop()
        if (!runningRef.current) { playSendCue(); sendText(dictBase.current.trim()) }
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
  // The hotword was heard and the mic now listens: a cue says so.
  const prevMicState = useRef(mic.state)
  useEffect(() => {
    if (mic.state === 'awake' && prevMicState.current !== 'awake') playListenCue()
    prevMicState.current = mic.state
  }, [mic.state])
  const hotword = getHotword()
  const pushToTalk = getSttEngine() === 'orchard'
  const micTitle = {
    off: pushToTalk ? 'Dictate (Orchard): click, speak, click again' : hotword.enabled ? `Listen for the hotword${hotword.backend === 'vosk' ? ` “${hotword.phrase}”` : ' “hey jarvis”'}` : 'Dictate (Parakeet, local)',
    loading: 'Loading the voice model…',
    listening: mic.engine === 'orchard' ? 'Recording: click to stop and transcribe' : 'Stop dictating',
    armed: 'Waiting for the hotword (click to stop)',
    awake: 'Listening for your command…',
    processing: 'Transcribing…',
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
  // The Supervisor tab shows the history beside the chat (it can be hidden); elsewhere it is a popover.
  const isMobile = useIsMobile()
  const hasSidebar = (full || !!embed) && !isMobile
  const [historyOpen, setHistoryOpen] = useState(() => lsGet(LS.historyOpen, '1') === '1')
  const [historyVersion, setHistoryVersion] = useState(0)

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
  useEffect(() => { if (!embed) setOrchCache(cache) }, [cache, setOrchCache, embed])

  const toggle = (v: boolean) => setOpen(v)

  // The project's orchestrator chat.
  useEffect(() => {
    if (!projectId) { setSession(null); return }
    let cancelled = false
    setSession(null)
    setError('')
    const opening = embed ? openEmbedded(projectId, embed) : api.getOrchestrator(projectId)
    opening
      .then(async s => {
        if (cancelled) return
        await loadMessages(s.id)
        seedContextUsage(s)
        if (!cancelled) setSession({ id: s.id, last_active: s.message_count > 0 ? s.last_active : '' })
      })
      .catch(e => { if (!cancelled) setError(String(e?.message ?? e)) })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId, loadMessages, embed?.pluginId, embed?.model, embed?.rowId, embed?.source.id])

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
  const pluginUI = usePlugins(s => s.ui)
  const pluginsLoaded = usePlugins(s => s.loaded)
  const [model, setModel] = useState(embed?.model ?? 'sonnet')
  useEffect(() => {
    if (embed) { setModel(embed.model); return }
    if (!projectId) return
    const saved = normalizeModelId(lsGet(LS.model(projectId), 'sonnet'))
    setModel(isPluginModel(saved) || CLAUDE_MODELS.some(m => m.value === saved) ? saved : 'sonnet')
  }, [projectId, embed?.model]) // eslint-disable-line react-hooks/exhaustive-deps
  const setOrchModel = useStore(s => s.setOrchModel)
  useEffect(() => { if (!embed) setOrchModel(model) }, [model, setOrchModel, embed]) // the buttons that reopen the panel show its mark
  const answeredByPlugin = isPluginModel(model) // no cache, context or process to show
  const pluginModel = pluginUI.models.find(m => m.value === model)
  const modes = [SUPERVISOR_MODE, ...pluginUI.models.map(m => ({ value: m.value, label: m.mode, hint: m.hint || m.label }))]
  const pluginOfMode = (value: string) => pluginUI.models.find(m => m.value === value)
  const activeMode = answeredByPlugin ? model : SUPERVISOR_MODE.value
  const pickModel = (v: string) => {
    setModel(v)
    if (projectId && !embed) lsSet(LS.model(projectId), v)
  }
  // The Claude model to come back to when leaving a plugin's model.
  const lastClaudeModel = useRef('sonnet')
  if (!answeredByPlugin) lastClaudeModel.current = model
  const historySource: HistorySource = embed
    ? { kind: 'plugin', conversations: embed.source, page: true }
    : pluginModel ? { kind: 'plugin', conversations: conversationsOfModel(pluginUI, pluginModel) } : { kind: 'supervisor' }
  // Said once when a saved plugin model is gone: the plugin is off or removed.
  const [modelGone, setModelGone] = useState('')
  const [modeOpen, setModeOpen] = useState(false)
  const [switchingMode, setSwitchingMode] = useState(false)
  // An empty chat left behind by changing mode, used again instead of creating one per change.
  const emptyChat = useRef<{ projectId: string; id: string } | null>(null)
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
  // A hub (--hub-only) is not in the list: it runs no claude, so "local" is not an option
  // and the chat starts on the first runner.
  const canRunLocally = !runnersLoaded || runners.some(r => r.self)
  useEffect(() => {
    if (runnersLoaded && runnerId === 'local' && !runners.some(r => r.self) && runners.length > 0) setRunnerId(runners[0].id)
  }, [runnersLoaded, runners, runnerId])
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

  // Keep the newest message in view, except when older ones were just added above.
  const heightBeforeOlder = useRef<number | null>(null)
  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    if (heightBeforeOlder.current !== null) {
      el.scrollTop += el.scrollHeight - heightBeforeOlder.current
      heightBeforeOlder.current = null
      return
    }
    el.scrollTop = el.scrollHeight
  }, [sid, messages.length, streamingText, open, running])

  // A conversation opens on its latest messages; the older ones come on request.
  const hasOlder = useStore(s => (sid ? s.messagePagination[sid]?.hasMore : false) ?? false)
  const loadingOlder = useStore(s => (sid ? s.messagePagination[sid]?.isLoadingMore : false) ?? false)
  const loadMoreMessages = useStore(s => s.loadMoreMessages)
  const loadOlder = () => {
    if (!sid) return
    heightBeforeOlder.current = scrollRef.current?.scrollHeight ?? 0
    loadMoreMessages(sid).catch(e => {
      heightBeforeOlder.current = null
      useStore.setState(s => ({ messagePagination: { ...s.messagePagination, [sid]: { ...s.messagePagination[sid], isLoadingMore: false } } }))
      setError(String(e?.message ?? e))
    })
  }

  const runningRef = useRef(false)
  runningRef.current = running || switchingMode
  const imageRef = useRef(image)
  imageRef.current = image
  const attachFile = (file: File | null | undefined) => {
    if (!file) return
    if (!['image/jpeg', 'image/png', 'image/gif', 'image/webp'].includes(file.type)) {
      setError('Only JPEG, PNG, GIF and WebP images can be attached')
      return
    }
    const reader = new FileReader()
    reader.onload = () => setImage({ base64: String(reader.result).split(',')[1] ?? '', mediaType: file.type })
    reader.readAsDataURL(file)
  }
  const sendText = (typed: string) => {
    const img = imageRef.current
    const pending = embed ? [] : useStore.getState().orchRefs.filter(r => r.projectId === projectId)
    // an image or a reference alone is a message too
    if ((!typed && !img && pending.length === 0) || !sid || running || switchingMode || (runnerOffline && !answeredByPlugin)) return
    const text = [...pending.map(r => r.text), typed].filter(Boolean).join('\n\n')
    setInput('')
    setImage(null)
    if (projectId && !embed) clearRefs(projectId)
    dictBase.current = ''
    useStore.setState(s => ({ pendingSend: { ...s.pendingSend, [sid]: true } }))
    api.sendOrchestratorTurn(sid, text || '(see the attached image)', { runnerId, aiProfileId: effectiveAi, model }, img).catch(e => {
      useStore.setState(s => {
        const next = { ...s.pendingSend }
        delete next[sid]
        return { pendingSend: next, sessionError: { ...s.sessionError, [sid]: String(e?.message ?? e) } }
      })
    })
  }
  const send = () => sendText(input.trim())
  const canSend = !!(input.trim() || image || refs.length > 0)

  const cancel = () => { if (sid) api.cancelTurn(sid).catch(console.error) }

  // A conversation picked from the history: its chat comes back with its messages (or, if it
  // was an ordinary chat, it is opened). The next message continues it: with the plugin that holds
  // it in a plugin's mode, resuming the same claude conversation in Supervisor mode.
  const continueConversation = async (s: Session) => {
    if (!panelCanHold(s.kind)) { navigate(`/projects/${s.project_id}/sessions/${s.id}`); return }
    setError('')
    try {
      await showChat(s)
      setInput('')
      dictBase.current = ''
    } catch (e) {
      setError(String((e as Error)?.message ?? e))
    }
  }
  const showChat = async (s: Session) => {
    tts.stop()
    await loadMessages(s.id)
    seedContextUsage(s)
    setSession({ id: s.id, last_active: s.message_count > 0 ? s.last_active : '' })
  }

  // Each mode has its own conversations: changing mode brings back the latest one of the new
  // mode, or an empty chat if it has none, so claude and a plugin never answer in the same chat
  // by accident. What is typed in the box stays, to be sent in the new mode.
  const pickMode = async (v: string) => {
    setModeOpen(false)
    if (v === activeMode) return
    setModelGone('')
    const target = pluginUI.models.find(m => m.value === v)
    const nextModel = target ? target.value : lastClaudeModel.current
    const nextSource: HistorySource = target ? { kind: 'plugin', conversations: conversationsOfModel(pluginUI, target) } : { kind: 'supervisor' }
    if (!projectId || !sid) { pickModel(nextModel); return } // still loading: nothing to leave
    const leavingEmpty = messages.length === 0
    const stale = () => useStore.getState().activeProjectId !== projectId
    setSwitchingMode(true)
    setError('')
    try {
      const latest = await latestConversation(projectId, nextSource)
      let next: Session | null = null
      if (latest) {
        if (latest.sessionId !== sid) next = await latest.open()
      } else if (!leavingEmpty) {
        const empty = emptyChat.current?.projectId === projectId ? emptyChat.current.id : ''
        emptyChat.current = null
        next = empty
          ? await api.openOrchestratorChat(empty).catch(() => api.newOrchestratorSession(projectId))
          : await api.newOrchestratorSession(projectId)
      }
      if (stale()) return
      if (next) {
        await showChat(next)
        if (leavingEmpty) emptyChat.current = { projectId, id: sid }
      }
      pickModel(nextModel)
    } catch (e) {
      setError(String((e as Error)?.message ?? e)) // the mode stays as it was, with its chat
    } finally {
      setSwitchingMode(false)
    }
  }

  // The plugin behind a saved model was turned off or removed: back to the Supervisor, saying so,
  // with the Supervisor's own conversation, so it never answers inside the plugin's chat.
  const goneTried = useRef('') // once per model: a failed switch must not retry for ever
  useEffect(() => {
    if (embed || !pluginsLoaded || !answeredByPlugin || pluginModel || !sid || running || switchingMode) return
    if (goneTried.current === model) return
    goneTried.current = model
    const gone = parsePluginModel(model)
    void pickMode(SUPERVISOR_MODE.value).then(() => setModelGone(gone?.modelId ?? model))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [embed, pluginsLoaded, answeredByPlugin, pluginModel, sid, running, switchingMode, model])

  // A fresh conversation with an empty context window; the old one is closed.
  const startNewSession = async () => {
    if (!projectId || running) return
    if (messages.length > 0 && !confirm('Start a new conversation?\n\nThe current conversation is closed and its context is cleared.')) return
    tts.stop()
    setError('')
    try {
      const s = embed ? await api.createPluginConversation(embed.pluginId, projectId, embed.model) : await api.newOrchestratorSession(projectId)
      await loadMessages(s.id)
      setInput('')
      dictBase.current = ''
      setSession({ id: s.id, last_active: '' })
    } catch (e) {
      setError(String((e as Error)?.message ?? e))
    }
  }

  if (!projectId) return null

  // collapsed: the opener button is drawn by App (OrchestratorOpenButton), inside <main>
  if (!open) return null

  // Wheel outside the message column (side margins, header, input) scrolls the messages too,
  // unless the pointer is over something that scrolls by itself.
  const forwardWheel = (e: React.WheelEvent<HTMLElement>) => {
    const list = scrollRef.current
    if (!list || e.ctrlKey || list.contains(e.target as Node)) return
    for (let el = e.target as HTMLElement | null; el && el !== e.currentTarget; el = el.parentElement) {
      const oy = getComputedStyle(el).overflowY
      if ((oy === 'auto' || oy === 'scroll') && el.scrollHeight > el.clientHeight) return
    }
    list.scrollTop += e.deltaY
  }

  const historyProps = {
    projectId,
    source: historySource,
    currentId: sid,
    disabled: running || switchingMode,
    // a new chat enters the list with its first message, and its count changes when a turn ends
    reloadKey: `${sid}|${messages.length > 0}|${running}|${historyVersion}`,
    onContinue: continueConversation,
  }

  return (
    <aside className={`orch${full ? ' orch--full' : ''}${embed ? ' orch--embed' : ''}`} onWheel={forwardWheel}>
      <header className="orch__header">
        <ModelIcon model={model} size={17} className="orch__model-icon" />
        <span className="orch__title">
          {embed ? <span className="orch__fixed-title">{embed.label}</span> : <Popover open={modeOpen} onOpenChange={setModeOpen}>
            <PopoverTrigger asChild>
              <button type="button" className="orch__mode-btn" disabled={running || switchingMode} title="Mode" aria-label="Mode">
                {modes.find(m => m.value === activeMode)?.label ?? 'Supervisor'}
                {answeredByPlugin && <span className="orch__mode-plugin">plugin</span>}
                <IconChevronDown size={14} />
              </button>
            </PopoverTrigger>
            <PopoverContent side="bottom" align="start" className="env-picker">
              <div className="env-picker__section">
                <div className="env-picker__title">Mode</div>
                {modes.map((m, index) => {
                  const active = m.value === activeMode
                  const plugin = pluginOfMode(m.value)
                  return (
                    <Fragment key={m.value}>
                    {index === 1 && <div className="env-picker__divider" />}
                    {index === 1 && <div className="env-picker__title">From plugins</div>}
                    <button
                      type="button"
                      className={`env-picker__item${active ? ' env-picker__item--active' : ''}${plugin ? ' env-picker__item--plugin' : ''}`}
                      onClick={() => pickMode(m.value)}
                    >
                      <ModelIcon model={m.value === SUPERVISOR_MODE.value ? lastClaudeModel.current : m.value} size={16} className="env-picker__icon" />
                      <span className="env-picker__item-text">
                        <span>{m.label}</span>
                        <span className="env-picker__hint">{m.hint}</span>
                        {plugin && <span className="env-picker__origin">{pluginOriginLabel(plugin)}</span>}
                      </span>
                      {active && <IconCheck size={13} className="env-picker__check" />}
                    </button>
                    </Fragment>
                  )
                })}
              </div>
            </PopoverContent>
          </Popover>}
        </span>
        {embed && <PluginOrigin pluginId={embed.pluginId} className="orch__origin" />}
        {!answeredByPlugin && <CacheBadge cache={cache} />}
        {sid && !answeredByPlugin && <ClaudeProcess sessionId={sid} />}
        {sid && !embed && <OrchestratorEdit projectId={projectId} sessionId={sid} onSaved={() => setHistoryVersion(v => v + 1)} onMoved={s => { tts.stop(); navigate(`/projects/${s.project_id}/kanban`) }} />}
        {hasSidebar ? (
          <button
            className={`orch__icon-btn${historyOpen ? ' orch__icon-btn--on' : ''}`}
            onClick={() => { const v = !historyOpen; setHistoryOpen(v); lsSet(LS.historyOpen, v ? '1' : '0') }}
            title={historyOpen ? 'Hide the conversation history' : 'Show the conversation history'}
            aria-label="Conversation history"
            aria-pressed={historyOpen}
          >
            <IconHistory size={16} />
          </button>
        ) : <HistoryPopover {...historyProps} />}
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
        {!full && !embed && (
          <button className="orch__icon-btn" onClick={() => toggle(false)} title="Hide">
            <IconLayoutSidebarRightCollapse size={16} />
          </button>
        )}
      </header>

      <div className="orch__body">
      <div className="orch__main">
      {modelGone && (
        <div className="orch__notice" role="status">
          <IconInfoCircle size={15} aria-hidden />
          <span>The model “{modelGone}” is no longer available: its plugin is off or was removed. You are back on the Supervisor.</span>
          <button className="orch__ref-remove" onClick={() => setModelGone('')} aria-label="Dismiss"><IconX size={12} /></button>
        </div>
      )}
      {/* the side panel is too narrow for the details: they open in the middle of the screen */}
      {sid && !answeredByPlugin && <ContextBar sessionId={sid} placement={full ? 'panel' : 'viewport'} />}

      <div className="orch__messages" ref={scrollRef}>
        {hasOlder && (
          <button type="button" className="orch__older" onClick={loadOlder} disabled={loadingOlder}>
            {loadingOlder ? 'Loading…' : 'Load earlier messages'}
          </button>
        )}
        {error && <div className="orch__error">{error}</div>}
        {!error && messages.length === 0 && !running && (
          <div className="orch__empty">{answeredByPlugin ? `Ask ${pluginModel?.label ?? 'away'}.` : 'Tell me what you want done. I plan it on the board and hand it to the agents.'}</div>
        )}
        {messages.map(m => (
          <div key={m.id} className="orch__msg">
            <MessageBubble
              message={m}
              untrusted={answeredByPlugin}
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
        {image && (
          <div className="orch__image">
            <img src={`data:${image.mediaType};base64,${image.base64}`} alt="attached" />
            <button type="button" className="orch__image-remove" onClick={() => setImage(null)} title="Remove image" aria-label="Remove image"><IconX size={12} /></button>
          </div>
        )}
        {refs.length > 0 && (
          <ul className="orch__refs" aria-label="References to send">
            {refs.map(r => (
              <li key={r.id} className="orch__ref" title={`${r.label}\n${r.detail}`}>
                <IconCode size={13} aria-hidden />
                <span className="orch__ref-label">{r.label}</span>
                <button type="button" className="orch__ref-remove" onClick={() => removeRef(r.id)} aria-label={`Remove the reference ${r.label}`}><IconX size={12} /></button>
              </li>
            ))}
          </ul>
        )}
        <textarea
          ref={textareaRef}
          className="orch__textarea"
          rows={3}
          value={input}
          placeholder={!sid ? 'Loading…' : refs.length > 0 ? 'What do you want to know about this?' : answeredByPlugin ? `Message ${pluginModel?.label ?? 'the model'}…` : 'Message the orchestrator…'}
          disabled={!sid}
          onChange={e => {
            setInput(e.target.value)
            // typing while dictating: the dictation continues after what is now in the box
            if (mic.state !== 'off') dictBase.current = e.target.value
          }}
          onPaste={e => {
            const f = [...(e.clipboardData?.items ?? [])].find(i => i.type.startsWith('image/'))?.getAsFile()
            if (f) { e.preventDefault(); attachFile(f) }
          }}
          onKeyDown={e => {
            if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() }
            else if (e.key === 'Escape' && running) { e.preventDefault(); cancel() }
          }}
        />
        <div className="orch__controls">
          {/* "default" stands for no preconfigured AI (stored as an empty id) */}
          {!answeredByPlugin && <EnvPicker
            runner={runnerId}
            runners={[
              ...(canRunLocally ? [{ value: 'local', label: selfName }] : []),
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
            models={CLAUDE_MODELS}
            onModel={pickModel}
          />}
          <span className="orch__env-summary" title={answeredByPlugin ? pluginModel?.hint || pluginModel?.label : 'Runner · AI · Model'}>
            {answeredByPlugin
              ? <>{pluginModel && pluginOriginLabel(pluginModel)}{pluginModel?.hint ? ` · ${pluginModel.hint}` : ''}</>
              : <>{runnerName}{runnerOffline ? ' (offline)' : ''} · {profiles.find(p => p.id === effectiveAi)?.name ?? 'default'} · {CLAUDE_MODELS.find(m => m.value === model)?.label ?? model}</>}
          </span>
          <input ref={fileRef} type="file" accept="image/jpeg,image/png,image/gif,image/webp" hidden onChange={e => { attachFile(e.target.files?.[0]); e.target.value = '' }} />
          {(!answeredByPlugin || pluginModel?.images) && <button className={`orch__icon-btn${image ? ' orch__icon-btn--on' : ''}`} onClick={() => fileRef.current?.click()} disabled={!sid || running} title="Attach an image (or paste one)" aria-label="Attach an image">
            <IconPaperclip size={16} />
          </button>}
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
            <button className="orch__send" onClick={send} disabled={!canSend || !sid || (runnerOffline && !answeredByPlugin)} title={runnerOffline && !answeredByPlugin ? 'The selected runner is offline' : 'Send (Enter)'}><IconSend size={15} /></button>
          )}
        </div>
      </div>
      </div>
      {hasSidebar && historyOpen && <HistorySidebar {...historyProps} />}
      </div>
      {voiceOpen && <VoiceSettingsDialog onClose={() => setVoiceOpen(false)} onSaved={setTtsConfigured} />}
    </aside>
  )
}

