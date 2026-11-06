import { withPendingHooks } from './pendingHooks'
import { create } from 'zustand'
import type { Project, Session, Message, PendingHook, WSEvent, UserTool, ContextUsage, ClaudeProcess, InstanceInfo, RawEventEntry, ToolInputField } from '../types'
import * as api from '../api'
import type { CacheCountdown } from '../components/Chat/CacheBadge'

export interface TokenUsage {
  input_tokens: number
  output_tokens: number
  cache_creation_input_tokens?: number
  cache_read_input_tokens?: number
  cache_creation_1h_input_tokens?: number // of the written ones, the 1 hour cache
}

// Pending usage collected during a streaming turn (not exposed directly to components)
interface PendingUsage {
  inputUsage?: TokenUsage
  outputTokens?: number
}

interface MessagePagination {
  hasMore: boolean
  oldestSeq: number | null
  isLoadingMore: boolean
}

export interface FilePreview {
  filePath: string
  lang: string
  mode: 'content' | 'diff'
  content?: string      // mode === 'content'
  original?: string     // mode === 'diff'
  modified?: string     // mode === 'diff'
}

interface AppState {
  projects: Project[]
  sessions: Session[]
  // Bumped when an agent changes a task (done/blocked/comment) so open boards refresh.
  boardVersion: number
  flowVersion: number // bumped when a flow run changes, so the views refetch
  activeProjectId: string | null
  activeSessionId: string | null
  pendingSession: { projectId: string; model: string } | null
  messages: Record<string, Message[]>
  messagePagination: Record<string, MessagePagination>
  // Queue of pending hooks per session — supports concurrent parallel tool calls
  pendingHooks: Record<string, PendingHook[]>
  sessionStatus: Record<string, string>
  sessionError: Record<string, string>
  // Plain strings — Zustand Object.is comparison on strings is reliable
  streamingText: Record<string, string>
  streamingThinking: Record<string, string>
  sessionUsage: Record<string, TokenUsage>
  // ID of the last assistant message that arrived live via WebSocket (not loaded from REST)
  liveMessageId: Record<string, string>
  // Sessions where user sent a message but server hasn't echoed it back yet
  pendingSend: Record<string, boolean>
  filePreview: FilePreview | null
  starredMessages: Record<string, Message[]>
  starredPanelOpen: boolean
  // The orchestrator chat panel: open or collapsed (remembered), and its prompt-cache clock so the
  // button that reopens it can show the time left wherever that button is.
  orchOpen: boolean
  setOrchOpen: (open: boolean) => void
  orchCache: CacheCountdown | null
  setOrchCache: (c: CacheCountdown) => void
  jumpToMessageId: string | null
  userTools: UserTool[]
  // Context window usage per session (updated after each turn via WS)
  contextUsage: Record<string, ContextUsage>
  claudeProcess: Record<string, ClaudeProcess>
  // what a running turn is doing right now, when it says (e.g. reopening a long saved conversation)
  turnProgress: Record<string, string>
  // the model the orchestrator is set to, so every button that opens it can show its mark
  orchModel: string
  setOrchModel: (model: string) => void

  // Raw event modal state — which session/turn to inspect
  rawEventsModal: { sessionId: string; turnIndex?: number } | null
  rawEventsRevision: Record<string, number>
  openRawEvents: (sessionId: string, turnIndex?: number) => void
  closeRawEvents: () => void

  openFilePreview: (preview: FilePreview) => void
  closeFilePreview: () => void

  // File browser drawer (right-side panel listing files in the project's
  // workDir). Independent of the file preview — the user can browse with
  // the drawer open and then click a file to slide the preview in on top.
  fileBrowserOpen: boolean
  openFileBrowser: () => void
  closeFileBrowser: () => void

  toggleStar: (messageId: string, sessionId: string) => Promise<void>
  loadStarredMessages: (sessionId: string) => Promise<void>
  openStarredPanel: () => void
  closeStarredPanel: () => void
  setJumpToMessage: (id: string | null) => void
  loadMessageContext: (messageId: string, sessionId: string) => Promise<void>
  loadUserTools: () => Promise<void>
  createUserTool: (data: Partial<UserTool>) => Promise<UserTool>
  updateUserTool: (id: string, data: Partial<UserTool>) => Promise<UserTool>
  deleteUserTool: (id: string) => Promise<void>
  // Remote spaces / discovery
  instances: { self: InstanceInfo | null; peers: InstanceInfo[] }
  peersVersion: number // bumped when a runner appears or leaves, so lists of runners refetch
  activeRemoteId: string | null
  loadInstances: () => Promise<void>
  setActiveRemote: (id: string | null) => void
  setProjectAIAPI: (projectId: string, aiapiInstanceId: string) => Promise<void>

  loadProjects: () => Promise<void>
  createProject: (data: Partial<Project>) => Promise<Project>
  deleteProject: (id: string) => Promise<void>
  renameProject: (id: string, name: string) => Promise<void>
  setActiveProject: (id: string) => Promise<void>
  setActiveSession: (id: string) => Promise<void>
  openTaskSession: (cardId: string) => Promise<Session>
  setPendingSession: (projectId: string, model: string) => void
  clearPendingSession: () => void
  createSession: (pid: string, data: { name?: string; model?: string; provider?: string }) => Promise<Session>
  importSession: (pid: string, data: unknown) => Promise<Session>
  deleteSession: (id: string) => Promise<void>
  sendMessage: (id: string, msg: string, model?: string, effort?: string, thinking?: string, image?: { base64: string; mediaType: string } | null, provider?: string) => void
  respondHook: (sessId: string, decision: 'approve' | 'deny', reason?: string) => void
  forkSession: (id: string, name: string) => Promise<Session>
  renameSession: (id: string, name: string) => Promise<void>
  loadMessages: (id: string) => Promise<void>
  loadMoreMessages: (id: string) => Promise<void>
  handleWSEvent: (event: WSEvent) => void,
  increaseSessionMessageCount: (id:string)=>void
}

// Mutable maps outside Zustand — no re-render needed
const pendingUsageMap: Record<string, PendingUsage> = {}
// sessId → blockIndex → 'text' | 'thinking'
const blockTypeMap: Record<string, Record<number, string>> = {}
// Per-session flag: when Claude CLI / aiapi send both message_ready and
// turn_usage, only one path should add to sessionUsage. The first path
// to commit flips this to true; the other becomes a no-op.
const sessionUsageSettled: Record<string, boolean> = {}

// Raw WS event buffers (exported for RawEventsModal to read directly)
export const rawEventsBuf: Record<string, RawEventEntry[]> = {}
export const turnCounterMap: Record<string, number> = {}
export const messageToTurnMap: Record<string, number> = {} // assistantMsgId → turnIndex

// Helper: truncate message to title
function toTitle(msg: string): string {
  const trimmed = msg.trim().replace(/\s+/g, ' ')
  return trimmed.length > 50 ? trimmed.slice(0, 50) + '…' : trimmed
}

// ---------------------------------------------------------------------------
// replayStoredMessages
// ---------------------------------------------------------------------------
// Messages stored in the DB may have content = raw stream-event JSON
// (e.g. '{"type":"stream_event","event":{"type":"content_block_delta",...}}').
// This function replays them through the same state-machine as handleWSEvent
// so the result is the same list of clean Message objects the live WS path
// produces.  Messages that are already clean (plain text) pass through unchanged.
// ---------------------------------------------------------------------------
function replayStoredMessages(raw: Message[]): Message[] {
  const result: Message[] = []
  let text = ''
  let thinking = ''
  const blockType: Record<number, string> = {}
  let inputUsage: TokenUsage | undefined
  let outputTokens: number | undefined
  let baseMsg: Message | null = null

  for (const msg of raw) {
    // User messages are always plain text — pass through.
    if (msg.role === 'user') {
      result.push(msg)
      continue
    }

    // Detect old-format JSON content.
    let parsed: Record<string, unknown> | null = null
    if (msg.content.startsWith('{')) {
      try { parsed = JSON.parse(msg.content) } catch { /* not JSON */ }
    }

    // Already clean (new backend format or WS-synthesized message) — pass through.
    if (!parsed || (parsed.type !== 'stream_event' && parsed.type !== 'thinking')) {
      result.push(msg)
      continue
    }

    // Standalone thinking event (some claude versions emit {"type":"thinking",...})
    if (parsed.type === 'thinking') {
      const t = parsed.thinking as string | undefined
      if (t) result.push({ ...msg, role: 'thinking', content: t })
      continue
    }

    // stream_event wrapper — extract the inner Claude API event
    const inner = parsed.event as Record<string, unknown> | undefined
    if (!inner) continue

    switch (inner.type as string) {
      case 'message_start':
        text = ''
        thinking = ''
        for (const k in blockType) delete blockType[+k]
        inputUsage = (inner.message as Record<string, unknown>)?.usage as TokenUsage | undefined
        outputTokens = undefined
        baseMsg = msg
        break

      case 'content_block_start':
        blockType[inner.index as number] =
          ((inner.content_block as Record<string, unknown>)?.type as string) ?? 'text'
        break

      case 'content_block_delta': {
        const delta = inner.delta as Record<string, unknown> | undefined
        if (delta?.type === 'text_delta')     text     += (delta.text     as string) ?? ''
        if (delta?.type === 'thinking_delta') thinking += (delta.thinking as string) ?? ''
        break
      }

      case 'content_block_stop': {
        const idx = inner.index as number
        if (blockType[idx] === 'thinking' && thinking) {
          result.push({
            id: `${msg.id}-think`,
            session_id: msg.session_id,
            role: 'thinking',
            content: thinking,
            seq: msg.seq,
            created_at: msg.created_at,
          })
          thinking = ''
        }
        delete blockType[idx]
        break
      }

      case 'message_delta':
        outputTokens = ((inner.usage as Record<string, unknown>)?.output_tokens as number) ?? undefined
        break

      case 'message_stop': {
        if (text) {
          const usage: Message['usage'] = inputUsage
            ? {
                input_tokens:                  inputUsage.input_tokens ?? 0,
                output_tokens:                 outputTokens ?? 0,
                cache_creation_input_tokens:   inputUsage.cache_creation_input_tokens,
                cache_read_input_tokens:       inputUsage.cache_read_input_tokens,
              }
            : undefined
          result.push({
            id: baseMsg?.id ?? msg.id,
            session_id: msg.session_id,
            role: 'assistant',
            content: text,
            seq: baseMsg?.seq ?? msg.seq,
            created_at: baseMsg?.created_at ?? msg.created_at,
            usage,
          })
          text = ''
        }
        inputUsage = undefined
        outputTokens = undefined
        baseMsg = null
        break
      }
      // All other inner event types (rate_limit_event, etc.) are silent — skip.
    }
  }

  return result
}

export const useStore = create<AppState>((set, get) => ({
  instances: { self: null, peers: [] },
  peersVersion: 0,
  activeRemoteId: null,
  projects: [],
  sessions: [],
  boardVersion: 0,
  flowVersion: 0,
  activeProjectId: null,
  activeSessionId: null,
  pendingSession: null,
  messages: {},
  messagePagination: {},
  pendingHooks: {},
  sessionStatus: {},
  sessionError: {},
  streamingText: {},
  streamingThinking: {},
  sessionUsage: {},
  liveMessageId: {},
  pendingSend: {},
  filePreview: null,
  starredMessages: {},
  starredPanelOpen: false,
  orchOpen: (() => { try { return localStorage.getItem('orch_open') !== '0' } catch { return true } })(),
  setOrchOpen: (open) => {
    try { localStorage.setItem('orch_open', open ? '1' : '0') } catch { /* private mode */ }
    set({ orchOpen: open })
  },
  orchCache: null,
  setOrchCache: (c) => set(s => (s.orchCache && s.orchCache.label === c.label && s.orchCache.state === c.state ? s : { orchCache: c })),
  jumpToMessageId: null,
  userTools: [],
  contextUsage: {},
  claudeProcess: {},
  turnProgress: {},
  orchModel: 'sonnet',
  setOrchModel: (model) => set(s => (s.orchModel === model ? s : { orchModel: model })),
  rawEventsModal: null,
  rawEventsRevision: {},

  openRawEvents: (sessionId, turnIndex) => set({ rawEventsModal: { sessionId, turnIndex } }),
  closeRawEvents: () => set({ rawEventsModal: null }),

  openFilePreview: (preview) => set({ filePreview: preview }),
  closeFilePreview: () => set({ filePreview: null }),

  fileBrowserOpen: false,
  openFileBrowser: () => set({ fileBrowserOpen: true }),
  closeFileBrowser: () => set({ fileBrowserOpen: false }),
  openStarredPanel: () => set({ starredPanelOpen: true }),
  closeStarredPanel: () => set({ starredPanelOpen: false }),
  setJumpToMessage: (id) => set({ jumpToMessageId: id }),

  loadUserTools: async () => {
    const tools = await api.listUserTools()
    set({ userTools: tools })
  },

  createUserTool: async (data) => {
    const tool = await api.createUserTool(data)
    set(s => ({ userTools: [tool, ...s.userTools] }))
    return tool
  },

  updateUserTool: async (id, data) => {
    const tool = await api.updateUserTool(id, data)
    set(s => ({ userTools: s.userTools.map(t => t.id === id ? tool : t) }))
    return tool
  },

  deleteUserTool: async (id) => {
    await api.deleteUserTool(id)
    set(s => ({ userTools: s.userTools.filter(t => t.id !== id) }))
  },

  loadInstances: async () => {
    try {
      const data = await api.getInstances()
      set({ instances: { self: data.self, peers: data.peers ?? [] } })
    } catch {}
  },

  setActiveRemote: (id) => {
    api.setActiveRemoteId(id)
    set({ activeRemoteId: id, projects: [], sessions: [], activeProjectId: null, activeSessionId: null })
    if (id !== null) {
      api.listProjects().then(projects => set({ projects })).catch(() => {})
    } else {
      api.listProjects().then(projects => set({ projects })).catch(() => {})
    }
  },

  setProjectAIAPI: async (projectId, aiapiInstanceId) => {
    await api.setProjectAIAPI(projectId, aiapiInstanceId)
    set(s => ({
      projects: s.projects.map(p =>
        p.id === projectId ? { ...p, aiapi_instance_id: aiapiInstanceId } : p
      ),
    }))
  },

  loadProjects: async () => {
    const projects = await api.listProjects()
    set({ projects })
  },

  createProject: async (data) => {
    const project = await api.createProject(data)
    set(s => ({ projects: [project, ...s.projects] }))
    return project
  },

  deleteProject: async (id) => {
    await api.deleteProject(id)
    set(s => ({
      projects: s.projects.filter(p => p.id !== id),
      sessions: s.sessions.filter(sess => sess.project_id !== id),
      activeProjectId: s.activeProjectId === id ? null : s.activeProjectId,
    }))
  },

  renameProject: async (id, name) => {
    await api.renameProject(id, name)
    set(s => ({
      projects: s.projects.map(p => p.id === id ? { ...p, name } : p),
    }))
  },

  setActiveProject: async (id) => {
    set({ activeProjectId: id, activeSessionId: null })
    const sessions = await api.listSessions(id)
    // Restore persisted context usage from each session
    const restoredContextUsage: Record<string, ContextUsage> = {}
    for (const s of sessions) {
      if (s.context_usage) {
        try {
          const cu = JSON.parse(s.context_usage)
          if (cu && cu.totalTokens != null) {
            restoredContextUsage[s.id] = { ...cu, session_id: s.id }
          }
        } catch { /* ignore malformed */ }
      }
    }
    set(prev => ({
      sessions,
      contextUsage: { ...prev.contextUsage, ...restoredContextUsage },
    }))
  },

  setActiveSession: async (id) => {
    console.log('[store] setActiveSession called:', id)
    set({ activeSessionId: id })
    const session = get().sessions.find(s => s.id === id)

    if (session && session.message_count === 0) {
      // Brand-new empty session — no messages to load, WS will deliver them
      set(s => ({
        messages: { ...s.messages, [id]: withPendingHooks([], s.pendingHooks[id] ?? []) },
        messagePagination: {
          ...s.messagePagination,
          [id]: { hasMore: false, oldestSeq: null, isLoadingMore: false },
        },
      }))
    } else {
      // Existing session (session not in store yet on direct URL load, or has messages) — load from API
      await get().loadMessages(id)

      // Load raw events historical data for the session
      try {
        const stored = await api.listRawEvents(id)
        rawEventsBuf[id] = stored.map((e: any) => ({
          id: e.id,
          turnIndex: e.turn_index,
          timestamp: e.created_at,
          event: e.event as WSEvent,
        }))
        // Reconstruir turnCounterMap: el turno activo es el max turnIndex + 1
        if (stored.length > 0) {
          const maxTurn = Math.max(...stored.map((e: any) => e.turn_index))
          turnCounterMap[id] = maxTurn + 1
        }
        // Disparar re-render si el modal de esta sesión está abierto
        const state = get()
        if (state.rawEventsModal?.sessionId === id) {
          set(s => ({ rawEventsRevision: { ...s.rawEventsRevision, [id]: (s.rawEventsRevision[id] ?? 0) + 1 } }))
        }
      } catch {
        // Silent fail if raw events fail to load (feature)
      }
    }
  },

  setPendingSession: (projectId, model) => {
    set({ pendingSession: { projectId, model }, activeSessionId: null })
  },

  clearPendingSession: () => {
    set({ pendingSession: null })
  },

  // Opens (creating on first use) the chat session of a task and makes it active.
  openTaskSession: async (cardId) => {
    const session = await api.openCardSession(cardId)
    set(s => ({
      sessions: [session, ...s.sessions.filter(x => x.id !== session.id)],
      sessionStatus: { ...s.sessionStatus, [session.id]: session.status },
    }))
    await get().setActiveSession(session.id)
    return session
  },

  createSession: async (pid, data) => {
    const session = await api.createSession(pid, data)
    set(s => ({
      sessions: [session, ...s.sessions],
      activeSessionId: session.id, // Auto-select the new session so UI receives WS events
      sessionStatus: { ...s.sessionStatus, [session.id]: session.status },
    }))
    return session
  },

  importSession: async (pid, data) => {
    const session = await api.importSession(pid, data)
    set(s => ({
      sessions: [session, ...s.sessions],
      activeSessionId: session.id,
      sessionStatus: { ...s.sessionStatus, [session.id]: session.status },
    }))
    await get().loadMessages(session.id)
    return session
  },

  deleteSession: async (id) => {
    await api.deleteSession(id)
    set(s => ({
      sessions: s.sessions.filter(sess => sess.id !== id),
      activeSessionId: s.activeSessionId === id ? null : s.activeSessionId,
    }))
  },

  increaseSessionMessageCount: (id) => {
    set(s => ({
      sessions: s.sessions.map(sess => sess.id === id ? { ...sess, message_count: sess.message_count + 1 } : sess),
    }))
  },

  sendMessage: (id, msg, model, effort, thinking, image, provider) => {
    const pending = get().pendingSession

    // If sending first message in a pending session: create session first, then send turn
    if (pending) {
      api.createSession(pending.projectId, {
        name: toTitle(msg),
        model: model || pending.model,
        provider: provider || undefined,
      }).then(session => {
        set(s => ({
          sessions: [session, ...s.sessions],
          activeSessionId: session.id,
          pendingSession: null,
          pendingSend: { ...s.pendingSend, [session.id]: true },
        }))
        // Send the first message as a normal turn
        api.sendTurn(session.id, msg, model || pending.model, provider || undefined, effort, thinking, image)
          .catch(err => {
            set(s => {
              const next = { ...s.pendingSend }
              delete next[session.id]
              return { pendingSend: next }
            })
            console.error(err)
          })
      }).catch(console.error)
      return
    }

    // Normal sendTurn for existing sessions — show loader until server echoes user_message
    set(s => ({ pendingSend: { ...s.pendingSend, [id]: true } }))
    api.sendTurn(id, msg, model, provider || undefined, effort, thinking, image).catch(err => {
      // On error, clear the pending state
      set(s => {
        const next = { ...s.pendingSend }
        delete next[id]
        return { pendingSend: next }
      })
      console.error(err)
    })
  },

  respondHook: (_sessId, _decision, _reason) => {
    // Hook queue is drained via hook_resolved WS events; nothing to do here
  },

  forkSession: async (id, name) => {
    const session = await api.forkSession(id, name)
    set(s => ({ sessions: [session, ...s.sessions] }))
    return session
  },

  renameSession: async (id, name) => {
    await api.renameSession(id, name)
    set(s => ({
      sessions: s.sessions.map(sess =>
        sess.id === id ? { ...sess, name } : sess
      ),
    }))
  },

  loadMessages: async (id) => {
    console.log('[store] loadMessages called for:', id)
    const page = await api.listMessages(id)
    console.log('[store] loadMessages page:', page)
    const sorted = replayStoredMessages(page.messages).sort((a, b) =>
      new Date(a.created_at).getTime() - new Date(b.created_at).getTime()
    )
    set(s => ({
      // The API has no pending approvals (they are stored once answered): put them back.
      messages: { ...s.messages, [id]: withPendingHooks(sorted, s.pendingHooks[id] ?? []) },
      messagePagination: {
        ...s.messagePagination,
        [id]: { hasMore: page.has_more, oldestSeq: page.oldest_seq, isLoadingMore: false },
      },
    }))
  },

  loadMoreMessages: async (id) => {
    const pagination = get().messagePagination[id]
    if (!pagination || !pagination.hasMore || pagination.isLoadingMore) return

    set(s => ({
      messagePagination: {
        ...s.messagePagination,
        [id]: { ...s.messagePagination[id], isLoadingMore: true },
      },
    }))

    const page = await api.listMessages(id, { beforeSeq: pagination.oldestSeq ?? undefined })

    set(s => {
      const combined = [...replayStoredMessages(page.messages), ...(s.messages[id] ?? [])]
      const sorted = combined.sort((a, b) =>
        new Date(a.created_at).getTime() - new Date(b.created_at).getTime()
      )
      return {
        messages: { ...s.messages, [id]: sorted },
        messagePagination: {
          ...s.messagePagination,
          [id]: { hasMore: page.has_more, oldestSeq: page.oldest_seq, isLoadingMore: false },
        },
      }
    })
  },

  toggleStar: async (messageId, sessionId) => {
    await api.toggleStarMessage(messageId)
    set(s => ({
      messages: {
        ...s.messages,
        [sessionId]: (s.messages[sessionId] ?? []).map(m =>
          m.id === messageId ? { ...m, starred: !m.starred } : m
        ),
      },
    }))
    // Reload starred messages list
    try {
      const starred = await api.listStarredMessages(sessionId)
      set(s => ({
        starredMessages: { ...s.starredMessages, [sessionId]: starred },
      }))
    } catch (e) {
      console.error('Failed to reload starred messages:', e)
    }
  },

  loadStarredMessages: async (sessionId) => {
    try {
      const starred = await api.listStarredMessages(sessionId)
      set(s => ({
        starredMessages: { ...s.starredMessages, [sessionId]: starred },
      }))
    } catch (e) {
      console.error('Failed to load starred messages:', e)
    }
  },

  loadMessageContext: async (messageId, sessionId) => {
    try {
      const contextMessages = await api.getMessageContext(messageId)
      set(s => {
        const existing = s.messages[sessionId] ?? []
        const existingIds = new Set(existing.map(m => m.id))
        const newMessages = contextMessages.filter(m => !existingIds.has(m.id))
        const combined = [...existing, ...newMessages]
        const sorted = combined.sort((a, b) => a.seq - b.seq)
        return {
          messages: { ...s.messages, [sessionId]: sorted },
        }
      })
    } catch (e) {
      console.error('Failed to load message context:', e)
    }
  },

  handleWSEvent: (event: WSEvent) => {
    // Collect raw event in mutable buffer (no re-render unless modal is open)
    {
      const sessId = event.type === 'session_updated'
        ? event.session.id
        : ('session_id' in event ? (event as { session_id: string }).session_id : undefined)
      if (sessId) {
        const turnIdx = turnCounterMap[sessId] ?? 0
        if (!rawEventsBuf[sessId]) rawEventsBuf[sessId] = []
        const buf = rawEventsBuf[sessId]
        buf.push({ id: `${Date.now()}-${Math.random().toString(36).slice(2, 6)}`, turnIndex: turnIdx, timestamp: new Date().toISOString(), event })
        if (buf.length > 1000) rawEventsBuf[sessId] = buf.slice(-1000)
        if (get().rawEventsModal?.sessionId === sessId) {
          set(s => ({ rawEventsRevision: { ...s.rawEventsRevision, [sessId]: (s.rawEventsRevision[sessId] ?? 0) + 1 } }))
        }
      }
    }

    switch (event.type) {
      case 'user_message': {
        const sessId = event.session_id
        const userMsg: Message = {
          id: event.id,
          session_id: sessId,
          role: 'user',
          content: event.content,
          seq: event.seq,
          created_at: event.created_at,
        }
        set(s => {
          const nextPending = { ...s.pendingSend }
          delete nextPending[sessId]
          return {
            messages: { ...s.messages, [sessId]: [...(s.messages[sessId] ?? []), userMsg] },
            pendingSend: nextPending,
          }
        })
        break
      }

      case 'session_status': {
        const errMsg = event.status === 'error' ? (event.error ?? '') : ''
        set(s => ({
          sessionStatus: { ...s.sessionStatus, [event.session_id]: event.status },
          sessionError: { ...s.sessionError, [event.session_id]: errMsg },
          turnProgress: event.status === 'running' ? s.turnProgress : { ...s.turnProgress, [event.session_id]: '' },
          sessions: s.sessions.map(sess =>
            sess.id === event.session_id ? { ...sess, status: event.status as Session['status'] } : sess
          ),
        }))
        break
      }

      case 'stream_event': {
        const sessId = event.session_id
        let outer = event.event as Record<string, unknown>
        if (!outer) break

        // Claude CLI: event.event is the raw stdout line, which is itself a
        // {type:'stream_event', event:{...inner...}} wrapper. Extract the inner event.
        // Ollama: event.event is already the inner event directly.
        let inner: Record<string, unknown>
        if (outer.type === 'stream_event' && outer.event) {
          inner = outer.event as Record<string, unknown>
        } else {
          inner = outer
        }

        if (!inner) break

        const innerType = inner.type as string

        {
          if (innerType === 'message_start') {
            const msg = inner.message as Record<string, unknown> | undefined
            pendingUsageMap[sessId] = { inputUsage: msg?.usage as TokenUsage | undefined }
            blockTypeMap[sessId] = {}
            // New turn: usage accounting for the previous turn is settled.
            delete sessionUsageSettled[sessId]
            set(s => ({
              streamingText: { ...s.streamingText, [sessId]: '' },
              streamingThinking: { ...s.streamingThinking, [sessId]: '' },
            }))

          } else if (innerType === 'content_block_start') {
            const index = inner.index as number
            const block = inner.content_block as Record<string, unknown> | undefined
            if (!blockTypeMap[sessId]) blockTypeMap[sessId] = {}
            blockTypeMap[sessId][index] = (block?.type as string) ?? 'text'

          } else if (innerType === 'content_block_delta') {
            const delta = inner.delta as Record<string, unknown> | undefined
            const index = inner.index as number

            // Ensure blockTypeMap is initialised (Ollama skips message_start)
            if (!blockTypeMap[sessId]) blockTypeMap[sessId] = {}

            if (delta?.type === 'text_delta') {
              const chunk = (delta.text as string) ?? ''
              set(s => ({
                streamingText: {
                  ...s.streamingText,
                  [sessId]: (s.streamingText[sessId] ?? '') + chunk,
                },
              }))
            } else if (delta?.type === 'thinking_delta') {
              const chunk = (delta.thinking as string) ?? ''
              set(s => ({
                streamingThinking: {
                  ...s.streamingThinking,
                  [sessId]: (s.streamingThinking[sessId] ?? '') + chunk,
                },
              }))
            }
            void index

          } else if (innerType === 'text_delta') {
            // Ollama simplified format: { type: 'text_delta', delta: 'text...' }
            const chunk = typeof inner.delta === 'string' ? inner.delta : ''
            if (chunk) {
              if (!blockTypeMap[sessId]) blockTypeMap[sessId] = {}
              set(s => ({
                streamingText: {
                  ...s.streamingText,
                  [sessId]: (s.streamingText[sessId] ?? '') + chunk,
                },
              }))
            }

          } else if (innerType === 'content_block_stop') {
            const index = inner.index as number
            const blockType = blockTypeMap[sessId]?.[index]

            if (blockType === 'thinking') {
              // The thinking bubble was rendered live from streamingThinking;
              // on stop we just clear it. The persisted thinking message is
              // appended by the message_ready handler (which carries the
              // authoritative text from the server). Creating it here too
              // caused a duplicate thinking bubble.
              set(s => ({
                streamingThinking: { ...s.streamingThinking, [sessId]: '' },
              }))
            }
            if (blockTypeMap[sessId]) delete blockTypeMap[sessId][index]

          } else if (innerType === 'message_delta') {
            const usage = inner.usage as { output_tokens?: number } | undefined
            if (pendingUsageMap[sessId]) {
              pendingUsageMap[sessId].outputTokens = usage?.output_tokens
            }

          } else if (innerType === 'message_stop') {
            // The streaming bubble was rendered live from streamingText/thinking;
            // on stop we just clear them so the bubble disappears. The
            // authoritative final message is appended by the message_ready
            // handler (which carries the persisted text + id from the server).
            // Avoid creating a second bubble here — that caused the visible
            // duplicate after every turn.
            delete pendingUsageMap[sessId]
            delete blockTypeMap[sessId]
            set(s => ({
              streamingText: { ...s.streamingText, [sessId]: '' },
              streamingThinking: { ...s.streamingThinking, [sessId]: '' },
            }))
          }
        }
        break
      }

      case 'message_ready': {
        // Ollama sends message_ready when the full response is available.
        // Finalize streaming text (or fall back to event.message.content if streaming was empty).
        const sessId = event.session_id
        const ev = event as Record<string, unknown>
        const evMessage = ev.message as Record<string, unknown> | undefined
        const fallback = evMessage?.content as string
          || ev.content as string
          || ''
        const fallbackThinking = evMessage?.thinking as string
          || ev.thinking as string
          || ''
        const text = get().streamingText[sessId] || fallback
        const thinking = get().streamingThinking[sessId] || fallbackThinking
        const pending = pendingUsageMap[sessId]
        delete pendingUsageMap[sessId]
        delete blockTypeMap[sessId]

        const msgs = get().messages[sessId] ?? []
        const appended: Message[] = []

        // Persist the thinking block as its own message so it survives a
        // page refresh. The streaming view already shows it inline, but
        // the final Message list needs an explicit entry.
        if (thinking) {
          const thinkMsg: Message = {
            id: `think-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`,
            session_id: sessId,
            role: 'thinking',
            content: thinking,
            seq: msgs.length + appended.length,
            created_at: new Date().toISOString(),
          }
          messageToTurnMap[thinkMsg.id] = turnCounterMap[sessId] ?? 0
          appended.push(thinkMsg)
        }

        if (text) {
          const finalMsg: Message = {
            id: `ws-${Date.now()}`,
            session_id: sessId,
            role: 'assistant',
            content: text,
            seq: msgs.length + appended.length,
            created_at: new Date().toISOString(),
            usage: pending?.inputUsage
              ? {
                  input_tokens: pending.inputUsage.input_tokens ?? 0,
                  output_tokens: pending.outputTokens ?? 0,
                  cache_creation_input_tokens: pending.inputUsage.cache_creation_input_tokens,
                  cache_read_input_tokens: pending.inputUsage.cache_read_input_tokens,
                }
              : undefined,
          }
          messageToTurnMap[finalMsg.id] = turnCounterMap[sessId] ?? 0
          appended.push(finalMsg)
        }

        if (appended.length > 0) {
          // Accumulate usage from the finalized assistant message so the
          // header token badge stays accurate. Only count it here if the
          // server hasn't also sent a turn_usage event for the same turn
          // (Claude CLI / aiapi do both; Ollama only sends message_ready).
          const finalMsg = appended.find(m => m.role === 'assistant')
          const usage = finalMsg?.usage
          const shouldCountUsage = !!usage && !sessionUsageSettled[sessId]
          if (shouldCountUsage) sessionUsageSettled[sessId] = true
          set(s => ({
            messages: { ...s.messages, [sessId]: [...(s.messages[sessId] ?? []), ...appended] },
            streamingText: { ...s.streamingText, [sessId]: '' },
            streamingThinking: { ...s.streamingThinking, [sessId]: '' },
            liveMessageId: { ...s.liveMessageId, [sessId]: appended[appended.length - 1].id },
            ...(shouldCountUsage
              ? {
                  sessionUsage: {
                    ...s.sessionUsage,
                    [sessId]: {
                      input_tokens: (s.sessionUsage[sessId]?.input_tokens ?? 0) + usage!.input_tokens,
                      output_tokens: (s.sessionUsage[sessId]?.output_tokens ?? 0) + usage!.output_tokens,
                      cache_creation_input_tokens:
                        (s.sessionUsage[sessId]?.cache_creation_input_tokens ?? 0) +
                        (usage!.cache_creation_input_tokens ?? 0),
                      cache_read_input_tokens:
                        (s.sessionUsage[sessId]?.cache_read_input_tokens ?? 0) +
                        (usage!.cache_read_input_tokens ?? 0),
                    },
                  },
                }
              : {}),
          }))
        } else {
          set(s => ({
            streamingText: { ...s.streamingText, [sessId]: '' },
            streamingThinking: { ...s.streamingThinking, [sessId]: '' },
          }))
        }
        break
      }

      case 'flow_updated': {
        set(s => ({ flowVersion: s.flowVersion + 1 }))
        break
      }

      case 'task_updated': {
        set(s => ({ boardVersion: s.boardVersion + 1 }))
        break
      }

      case 'hook_pending': {
        const newHook: PendingHook = {
          session_id: event.session_id,
          tool_use_id: event.tool_use_id,
          tool_name: event.tool_name,
          tool_input: event.tool_input,
          tool_input_schema: (event as { tool_input_schema?: ToolInputField[] | null }).tool_input_schema ?? null,
          created_at: event.created_at,
        }
        set(s => {
          const existing = s.pendingHooks[event.session_id] ?? []
          const known = existing.some(h => h.tool_use_id === event.tool_use_id)
          const hooks = known ? existing : [...existing, newHook]
          // Also covers a replay for a hook we already know whose chat message was lost
          // when the session's messages were reloaded.
          const current = s.messages[event.session_id] ?? []
          const messages = withPendingHooks(current, hooks)
          if (known && messages === current) return s // reconnect replay, nothing to do
          return {
            pendingHooks: known ? s.pendingHooks : { ...s.pendingHooks, [event.session_id]: hooks },
            messages: { ...s.messages, [event.session_id]: messages },
          }
        })
        break
      }

      case 'hook_resolved': {
        set(s => {
          const existing = s.pendingHooks[event.session_id] ?? []
          const hookMsgId = `hook-${event.tool_use_id}`

          // Update virtual hook message with decision
          const updatedMessages = (s.messages[event.session_id] ?? []).map(msg =>
            msg.id === hookMsgId
              ? {
                  ...msg,
                  content: JSON.stringify({
                    tool_use_id: (JSON.parse(msg.content) as any).tool_use_id,
                    tool_name: (JSON.parse(msg.content) as any).tool_name,
                    tool_input: (JSON.parse(msg.content) as any).tool_input,
                    decision: event.decision,
                  }),
                }
              : msg
          )

          return {
            pendingHooks: {
              ...s.pendingHooks,
              [event.session_id]: existing.filter(h => h.tool_use_id !== event.tool_use_id),
            },
            messages: {
              ...s.messages,
              [event.session_id]: updatedMessages,
            },
          }
        })
        break
      }

      case 'turn_complete': {
        const tcSessId = event.session_id
        turnCounterMap[tcSessId] = (turnCounterMap[tcSessId] ?? 0) + 1
        set(s => {
          const extraMessages: Message[] = (event.result_text && event.result_subtype !== 'success')
            ? [{
                id: `tc-${Date.now()}`,
                session_id: tcSessId,
                role: 'system' as const,
                content: event.result_text,
                seq: (s.messages[tcSessId]?.length ?? 0),
                created_at: new Date().toISOString(),
              }]
            : []
          return {
            sessionStatus: { ...s.sessionStatus, [tcSessId]: 'idle' },
            sessions: s.sessions.map(sess =>
              sess.id === tcSessId ? { ...sess, status: 'idle' as const } : sess
            ),
            messages: extraMessages.length > 0
              ? { ...s.messages, [tcSessId]: [...(s.messages[tcSessId] ?? []), ...extraMessages] }
              : s.messages,
          }
        })
        break
      }

      case 'peers_changed':
        set(s => ({ peersVersion: s.peersVersion + 1 }))
        void get().loadInstances()
        break

      case 'error':
        set(s => ({
          // a task's error note is saved by the hub just before this event: re-read the board
          boardVersion: s.sessions.some(x => x.id === event.session_id && x.card_id) ? s.boardVersion + 1 : s.boardVersion,
          sessionStatus: { ...s.sessionStatus, [event.session_id]: 'error' },
          sessions: s.sessions.map(sess =>
            sess.id === event.session_id ? { ...sess, status: 'error' } : sess
          ),
          messages: {
            ...s.messages,
            [event.session_id]: [...(s.messages[event.session_id] ?? []), {
              id: `err-${Date.now()}`,
              session_id: event.session_id,
              role: 'system' as const,
              content: `Error: ${event.message}`,
              seq: (s.messages[event.session_id]?.length ?? 0),
              created_at: new Date().toISOString(),
            }],
          },
        }))
        break

      case 'tool_execution': {
        // Tool execution started — add to messages with loading state
        const sessId = event.session_id
        set(s => {
          const toolExecMsg: Message = {
            id: event.message_id,
            session_id: sessId,
            role: 'tool_execution',
            content: JSON.stringify({
              tool_name: event.tool_name,
              tool_input: event.tool_input,
              tool_use_id: event.tool_use_id,
              timestamp: event.timestamp,
            }),
            seq: (s.messages[sessId]?.length ?? 0),
            created_at: event.timestamp,
          }
          return {
            messages: {
              ...s.messages,
              [sessId]: [...(s.messages[sessId] ?? []), toolExecMsg],
            },
          }
        })
        break
      }

      case 'tool_execution_complete': {
        // Tool execution completed — update the message with response
        const sessId = event.session_id
        const msgId = event.message_id
        set(s => {
          const msgs = s.messages[sessId] ?? []
          const updated = msgs.map(m => {
            if (m.id === msgId) {
              const oldContent = JSON.parse(m.content) as Record<string, unknown>
              return {
                ...m,
                content: JSON.stringify({
                  ...oldContent,
                  tool_response: event.tool_response,
                }),
              }
            }
            return m
          })
          return {
            messages: { ...s.messages, [sessId]: updated },
          }
        })
        break
      }

      case 'info':
        // Non-fatal informational message — add to chat, don't change session status
        set(s => ({
          messages: {
            ...s.messages,
            [event.session_id]: [...(s.messages[event.session_id] ?? []), {
              id: `info-${Date.now()}`,
              session_id: event.session_id,
              role: 'system' as const,
              content: `ℹ ${event.message}`,
              seq: (s.messages[event.session_id]?.length ?? 0),
              created_at: new Date().toISOString(),
            }],
          },
        }))
        break

      case 'progress': {
        set(s => ({ turnProgress: { ...s.turnProgress, [event.session_id]: event.text } }))
        break
      }

      case 'claude_process': {
        set(s => ({ claudeProcess: { ...s.claudeProcess, [event.session_id]: { alive: event.alive, pid: event.pid, runner: event.runner, workspace: s.claudeProcess[event.session_id]?.workspace } } }))
        break
      }

      case 'claude_workspace': {
        const { type: _t, session_id, ...workspace } = event
        set(s => ({ claudeProcess: { ...s.claudeProcess, [session_id]: { ...s.claudeProcess[session_id], alive: s.claudeProcess[session_id]?.alive ?? false, workspace } } }))
        break
      }

      case 'context_usage': {
        set(s => ({
          contextUsage: {
            ...s.contextUsage,
            [event.session_id]: {
              categories: event.categories ?? [],
              totalTokens: event.totalTokens,
              maxTokens: event.maxTokens,
              percentage: event.percentage,
              model: event.model,
              session_id: event.session_id,
            },
          },
        }))
        break
      }

      case 'turn_usage': {
        // Server-authoritative token counts — update sessionUsage so the header badge is accurate.
        // Also embed tokens into live tool_execution messages so they display without a refresh.
        // If message_ready already counted this turn's usage, skip the
        // sessionUsage update to avoid double-counting.
        const tuSessId = event.session_id
        const tuUsage = {
          input_tokens: event.input_tokens,
          output_tokens: event.output_tokens,
          cache_creation_input_tokens: event.cache_creation_input_tokens,
          cache_read_input_tokens: event.cache_read_input_tokens,
        }
        const alreadySettled = sessionUsageSettled[tuSessId]
        if (!alreadySettled) sessionUsageSettled[tuSessId] = true
        set(s => {
          // Update tool_execution messages that don't yet have usage data
          const msgs = s.messages[tuSessId] ?? []
          const updatedMsgs = msgs.map(m => {
            if (m.role !== 'tool_execution') return m
            try {
              const data = JSON.parse(m.content) as Record<string, unknown>
              if (data.usage) return m // already set
              return { ...m, content: JSON.stringify({ ...data, usage: tuUsage }) }
            } catch { return m }
          })
          if (alreadySettled) {
            // Still update the per-message usage on tool_execution bubbles
            // so they reflect server-authoritative counts, just don't
            // re-add to sessionUsage.
            return { messages: { ...s.messages, [tuSessId]: updatedMsgs } }
          }
          const prev = s.sessionUsage[tuSessId] ?? { input_tokens: 0, output_tokens: 0 }
          return {
            sessionUsage: {
              ...s.sessionUsage,
              [tuSessId]: {
                input_tokens: prev.input_tokens + event.input_tokens,
                output_tokens: prev.output_tokens + event.output_tokens,
                cache_creation_input_tokens: (prev.cache_creation_input_tokens ?? 0) + event.cache_creation_input_tokens,
                cache_read_input_tokens: (prev.cache_read_input_tokens ?? 0) + event.cache_read_input_tokens,
                cache_creation_1h_input_tokens: (prev.cache_creation_1h_input_tokens ?? 0) + (event.cache_creation_1h_input_tokens ?? 0),
              },
            },
            messages: { ...s.messages, [tuSessId]: updatedMsgs },
          }
        })
        break
      }
    }
  },
}))
