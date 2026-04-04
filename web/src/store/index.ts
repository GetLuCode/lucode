import { create } from 'zustand'
import type { Project, Session, Message, PendingHook, WSEvent } from '../types'
import * as api from '../api'

export interface TokenUsage {
  input_tokens: number
  output_tokens: number
  cache_creation_input_tokens?: number
  cache_read_input_tokens?: number
}

export interface StreamingMessage {
  text: string
  inputUsage?: TokenUsage
  outputTokens?: number
}

interface AppState {
  projects: Project[]
  sessions: Session[]
  activeProjectId: string | null
  activeSessionId: string | null
  messages: Record<string, Message[]>
  pendingHook: PendingHook | null
  sessionStatus: Record<string, string>
  streaming: Record<string, StreamingMessage>     // sessionId → current streaming text
  sessionUsage: Record<string, TokenUsage>        // sessionId → cumulative totals

  loadProjects: () => Promise<void>
  createProject: (data: Partial<Project>) => Promise<Project>
  deleteProject: (id: string) => Promise<void>
  setActiveProject: (id: string) => Promise<void>
  setActiveSession: (id: string) => Promise<void>
  createSession: (pid: string, data: { message?: string; name?: string; model?: string }) => Promise<Session>
  deleteSession: (id: string) => Promise<void>
  sendMessage: (id: string, msg: string, model?: string) => void
  respondHook: (sessId: string, decision: 'approve' | 'deny', reason?: string) => void
  forkSession: (id: string, name: string) => Promise<Session>
  loadMessages: (id: string) => Promise<void>
  handleWSEvent: (event: WSEvent) => void
}

export const useStore = create<AppState>((set, get) => ({
  projects: [],
  sessions: [],
  activeProjectId: null,
  activeSessionId: null,
  messages: {},
  pendingHook: null,
  sessionStatus: {},
  streaming: {},
  sessionUsage: {},

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

  setActiveProject: async (id) => {
    set({ activeProjectId: id, activeSessionId: null })
    const sessions = await api.listSessions(id)
    set({ sessions })
  },

  setActiveSession: async (id) => {
    set({ activeSessionId: id })
    await get().loadMessages(id)
  },

  createSession: async (pid, data) => {
    const session = await api.createSession(pid, data)
    set(s => ({ sessions: [session, ...s.sessions] }))
    return session
  },

  deleteSession: async (id) => {
    await api.deleteSession(id)
    set(s => ({
      sessions: s.sessions.filter(sess => sess.id !== id),
      activeSessionId: s.activeSessionId === id ? null : s.activeSessionId,
    }))
  },

  sendMessage: (id, msg, model) => {
    api.sendTurn(id, msg, model).catch(console.error)
    const seq = (get().messages[id]?.length ?? 0)
    const userMsg: Message = {
      id: `opt-${Date.now()}`,
      session_id: id,
      role: 'user',
      content: msg,
      seq,
      created_at: new Date().toISOString(),
    }
    set(s => ({
      messages: { ...s.messages, [id]: [...(s.messages[id] ?? []), userMsg] },
    }))
  },

  respondHook: (_sessId, _decision, _reason) => {
    set({ pendingHook: null })
  },

  forkSession: async (id, name) => {
    const session = await api.forkSession(id, name)
    set(s => ({ sessions: [session, ...s.sessions] }))
    return session
  },

  loadMessages: async (id) => {
    const messages = await api.listMessages(id)
    set(s => ({ messages: { ...s.messages, [id]: messages } }))
  },

  handleWSEvent: (event: WSEvent) => {
    switch (event.type) {
      case 'session_status':
        set(s => ({
          sessionStatus: { ...s.sessionStatus, [event.session_id]: event.status },
          sessions: s.sessions.map(sess =>
            sess.id === event.session_id ? { ...sess, status: event.status as Session['status'] } : sess
          ),
        }))
        break

      case 'stream_event': {
        const sessId = event.session_id
        const raw = event.event as Record<string, unknown>
        if (!raw) break

        const outerType = raw.type as string

        // Streaming text accumulation
        if (outerType === 'stream_event') {
          const inner = (raw.event ?? raw) as Record<string, unknown>
          const innerType = inner?.type as string

          if (innerType === 'message_start') {
            // Start of a new assistant message — capture input token usage
            const msg = inner.message as Record<string, unknown> | undefined
            const usage = msg?.usage as TokenUsage | undefined
            set(s => ({
              streaming: {
                ...s.streaming,
                [sessId]: { text: '', inputUsage: usage },
              },
            }))
          } else if (innerType === 'content_block_delta') {
            const delta = inner.delta as Record<string, unknown> | undefined
            if (delta?.type === 'text_delta') {
              const text = delta.text as string ?? ''
              set(s => {
                const cur = s.streaming[sessId] ?? { text: '' }
                return { streaming: { ...s.streaming, [sessId]: { ...cur, text: cur.text + text } } }
              })
            }
          } else if (innerType === 'message_delta') {
            // Capture output token count
            const usage = inner.usage as { output_tokens?: number } | undefined
            set(s => {
              const cur = s.streaming[sessId] ?? { text: '' }
              return { streaming: { ...s.streaming, [sessId]: { ...cur, outputTokens: usage?.output_tokens } } }
            })
          } else if (innerType === 'message_stop') {
            // Finalize: move streaming text to messages
            const cur = get().streaming[sessId]
            if (cur?.text) {
              const msgs = get().messages[sessId] ?? []
              const finalMsg: Message = {
                id: `ws-${Date.now()}`,
                session_id: sessId,
                role: 'assistant',
                content: cur.text,
                seq: msgs.length,
                created_at: new Date().toISOString(),
                usage: cur.inputUsage
                  ? {
                      input_tokens: cur.inputUsage.input_tokens ?? 0,
                      output_tokens: cur.outputTokens ?? 0,
                      cache_creation_input_tokens: cur.inputUsage.cache_creation_input_tokens,
                      cache_read_input_tokens: cur.inputUsage.cache_read_input_tokens,
                    }
                  : undefined,
              }
              set(s => ({
                messages: { ...s.messages, [sessId]: [...(s.messages[sessId] ?? []), finalMsg] },
                streaming: { ...s.streaming, [sessId]: { text: '' } },
              }))

              // Accumulate session totals
              if (cur.inputUsage) {
                set(s => {
                  const prev = s.sessionUsage[sessId] ?? { input_tokens: 0, output_tokens: 0 }
                  return {
                    sessionUsage: {
                      ...s.sessionUsage,
                      [sessId]: {
                        input_tokens: prev.input_tokens + (cur.inputUsage?.input_tokens ?? 0),
                        output_tokens: prev.output_tokens + (cur.outputTokens ?? 0),
                        cache_creation_input_tokens:
                          (prev.cache_creation_input_tokens ?? 0) + (cur.inputUsage?.cache_creation_input_tokens ?? 0),
                        cache_read_input_tokens:
                          (prev.cache_read_input_tokens ?? 0) + (cur.inputUsage?.cache_read_input_tokens ?? 0),
                      },
                    },
                  }
                })
              }
            } else {
              set(s => ({ streaming: { ...s.streaming, [sessId]: { text: '' } } }))
            }
          }
        }

        // For tool_use, thinking, rate_limit_event, and anything unhandled — add as raw message
        if (
          outerType === 'thinking' ||
          outerType === 'rate_limit_event' ||
          (outerType === 'stream_event' &&
            !['message_start', 'content_block_start', 'content_block_delta',
              'content_block_stop', 'message_delta', 'message_stop'].includes(
              ((raw.event ?? raw) as Record<string, unknown>)?.type as string
            ))
        ) {
          const msgs = get().messages[sessId] ?? []
          const rawMsg: Message = {
            id: `raw-${Date.now()}-${Math.random()}`,
            session_id: sessId,
            role: outerType === 'thinking' ? 'thinking' : 'raw',
            content: JSON.stringify(event.event, null, 2),
            seq: msgs.length,
            created_at: new Date().toISOString(),
          }
          set(s => ({
            messages: { ...s.messages, [sessId]: [...(s.messages[sessId] ?? []), rawMsg] },
          }))
        }
        break
      }

      case 'hook_pending':
        set({
          pendingHook: {
            session_id: event.session_id,
            tool_name: event.tool_name,
            tool_input: event.tool_input,
          },
        })
        break

      case 'hook_resolved':
        set({ pendingHook: null })
        break

      case 'turn_complete':
        set(s => ({
          sessionStatus: { ...s.sessionStatus, [event.session_id]: 'idle' },
          sessions: s.sessions.map(sess =>
            sess.id === event.session_id ? { ...sess, status: 'idle' } : sess
          ),
        }))
        break

      case 'error':
        set(s => ({
          sessionStatus: { ...s.sessionStatus, [event.session_id]: 'error' },
          sessions: s.sessions.map(sess =>
            sess.id === event.session_id ? { ...sess, status: 'error' } : sess
          ),
        }))
        break
    }
  },
}))
