import { create } from 'zustand'
import type { Project, Session, Message, PendingHook, WSEvent } from '../types'
import * as api from '../api'

export interface TokenUsage {
  input_tokens: number
  output_tokens: number
  cache_creation_input_tokens?: number
  cache_read_input_tokens?: number
}

// Pending usage collected during a streaming turn (not exposed directly to components)
interface PendingUsage {
  inputUsage?: TokenUsage
  outputTokens?: number
}

interface AppState {
  projects: Project[]
  sessions: Session[]
  activeProjectId: string | null
  activeSessionId: string | null
  messages: Record<string, Message[]>
  // Queue of pending hooks per session — supports concurrent parallel tool calls
  pendingHooks: Record<string, PendingHook[]>
  sessionStatus: Record<string, string>
  // Plain strings — Zustand Object.is comparison on strings is reliable
  streamingText: Record<string, string>
  streamingThinking: Record<string, string>
  sessionUsage: Record<string, TokenUsage>

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

// Mutable maps outside Zustand — no re-render needed
const pendingUsageMap: Record<string, PendingUsage> = {}
// sessId → blockIndex → 'text' | 'thinking'
const blockTypeMap: Record<string, Record<number, string>> = {}

export const useStore = create<AppState>((set, get) => ({
  projects: [],
  sessions: [],
  activeProjectId: null,
  activeSessionId: null,
  messages: {},
  pendingHooks: {},
  sessionStatus: {},
  streamingText: {},
  streamingThinking: {},
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
    // Hook queue is drained via hook_resolved WS events; nothing to do here
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

        if (outerType === 'stream_event') {
          const inner = (raw.event ?? raw) as Record<string, unknown>
          const innerType = inner?.type as string

          if (innerType === 'message_start') {
            const msg = inner.message as Record<string, unknown> | undefined
            pendingUsageMap[sessId] = { inputUsage: msg?.usage as TokenUsage | undefined }
            blockTypeMap[sessId] = {}
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

          } else if (innerType === 'content_block_stop') {
            const index = inner.index as number
            const blockType = blockTypeMap[sessId]?.[index]

            if (blockType === 'thinking') {
              // Finalize thinking block → add to messages list
              const thinkingText = get().streamingThinking[sessId] ?? ''
              if (thinkingText) {
                const msgs = get().messages[sessId] ?? []
                set(s => ({
                  messages: {
                    ...s.messages,
                    [sessId]: [...(s.messages[sessId] ?? []), {
                      id: `think-${Date.now()}`,
                      session_id: sessId,
                      role: 'thinking' as const,
                      content: thinkingText,
                      seq: msgs.length,
                      created_at: new Date().toISOString(),
                    }],
                  },
                  streamingThinking: { ...s.streamingThinking, [sessId]: '' },
                }))
              }
            }
            if (blockTypeMap[sessId]) delete blockTypeMap[sessId][index]

          } else if (innerType === 'message_delta') {
            const usage = inner.usage as { output_tokens?: number } | undefined
            if (pendingUsageMap[sessId]) {
              pendingUsageMap[sessId].outputTokens = usage?.output_tokens
            }

          } else if (innerType === 'message_stop') {
            // Finalize: move streaming text to messages list
            const text = get().streamingText[sessId] ?? ''
            const pending = pendingUsageMap[sessId]
            delete pendingUsageMap[sessId]
            delete blockTypeMap[sessId]

            if (text) {
              const msgs = get().messages[sessId] ?? []
              const usage: Message['usage'] = pending?.inputUsage
                ? {
                    input_tokens: pending.inputUsage.input_tokens ?? 0,
                    output_tokens: pending.outputTokens ?? 0,
                    cache_creation_input_tokens: pending.inputUsage.cache_creation_input_tokens,
                    cache_read_input_tokens: pending.inputUsage.cache_read_input_tokens,
                  }
                : undefined

              const finalMsg: Message = {
                id: `ws-${Date.now()}`,
                session_id: sessId,
                role: 'assistant',
                content: text,
                seq: msgs.length,
                created_at: new Date().toISOString(),
                usage,
              }

              set(s => ({
                messages: { ...s.messages, [sessId]: [...(s.messages[sessId] ?? []), finalMsg] },
                streamingText: { ...s.streamingText, [sessId]: '' },
                streamingThinking: { ...s.streamingThinking, [sessId]: '' },
              }))

              if (usage) {
                set(s => {
                  const prev = s.sessionUsage[sessId] ?? { input_tokens: 0, output_tokens: 0 }
                  return {
                    sessionUsage: {
                      ...s.sessionUsage,
                      [sessId]: {
                        input_tokens: prev.input_tokens + usage.input_tokens,
                        output_tokens: prev.output_tokens + usage.output_tokens,
                        cache_creation_input_tokens:
                          (prev.cache_creation_input_tokens ?? 0) + (usage.cache_creation_input_tokens ?? 0),
                        cache_read_input_tokens:
                          (prev.cache_read_input_tokens ?? 0) + (usage.cache_read_input_tokens ?? 0),
                      },
                    },
                  }
                })
              }
            } else {
              set(s => ({
                streamingText: { ...s.streamingText, [sessId]: '' },
                streamingThinking: { ...s.streamingThinking, [sessId]: '' },
              }))
            }
          }
        }
        break
      }

      case 'hook_pending': {
        const newHook: PendingHook = {
          session_id: event.session_id,
          tool_use_id: event.tool_use_id,
          tool_name: event.tool_name,
          tool_input: event.tool_input,
        }
        set(s => {
          const existing = s.pendingHooks[event.session_id] ?? []
          // Avoid duplicates (reconnect replay)
          if (existing.some(h => h.tool_use_id === event.tool_use_id)) return s
          return { pendingHooks: { ...s.pendingHooks, [event.session_id]: [...existing, newHook] } }
        })
        break
      }

      case 'hook_resolved':
        set(s => {
          const existing = s.pendingHooks[event.session_id] ?? []
          return {
            pendingHooks: {
              ...s.pendingHooks,
              [event.session_id]: existing.filter(h => h.tool_use_id !== event.tool_use_id),
            },
          }
        })
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
    }
  },
}))
