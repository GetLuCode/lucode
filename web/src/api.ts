import type { AIProfile, Runner, CardComment, Project, Session, MessagesPage, Message, SearchResult, UserTool, CommandDetectResult, ToolExecuteResult, InstanceInfo, ProcessInfo, Agent, KanbanList, KanbanCard, CheckpointListResponse, CheckpointWithTree, DiffResponse, FileDiff, Checkpoint, PermissionRule, PermissionScope } from './types'
import { useAuthStore } from './store/auth'

const BASE = import.meta.env.VITE_API_BASE ?? ''

// Active remote lucode instance — when set, all requests are proxied via NATS.
let activeRemoteId: string | null = null
export const setActiveRemoteId = (id: string | null) => { activeRemoteId = id }

function getAuthToken(): string | null {
  try {
    const raw = localStorage.getItem('lucode-auth')
    if (!raw) return null
    const parsed = JSON.parse(raw)
    return parsed?.state?.token ?? null
  } catch {
    return null
  }
}

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {}
  if (body) headers['Content-Type'] = 'application/json'

  const token = getAuthToken()
  if (token) headers['Authorization'] = `Bearer ${token}`
  if (activeRemoteId) headers['X-Lucode-Remote'] = activeRemoteId

  const res = await fetch(`${BASE}${path}`, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  })
  if (!res.ok) {
    if (res.status === 401) {
      useAuthStore.getState().clearAuth()
    }
    const text = await res.text()
    throw new Error(`${method} ${path} → ${res.status}: ${text}`)
  }
  if (res.status === 204 || res.headers.get('content-length') === '0') {
    return undefined as T
  }
  return res.json()
}

// Auth
export const getAuthStatus = () =>
  req<{ signup_open: boolean }>('GET', '/api/auth/status')
export const signup = (username: string, password: string) =>
  req<{ user_id: string; is_admin: boolean }>('POST', '/api/auth/signup', { username, password })
export const login = (username: string, password: string) =>
  req<{ token: string; user_id: string; is_admin: boolean }>('POST', '/api/auth/login', { username, password })
export const logout = () => req<void>('POST', '/api/auth/logout')

// API Keys
export const listAPIKeys = () =>
  req<{ id: string; name: string; prefix: string; created_at: string }[]>('GET', '/api/apikeys')
export const createAPIKey = (name: string) =>
  req<{ id: string; name: string; prefix: string; key: string; created_at: string }>('POST', '/api/apikeys', { name })
export const deleteAPIKey = (id: string) => req<void>('DELETE', `/api/apikeys/${id}`)

// MiniMax token plan
export const getMinimaxTokenPlan = () =>
  req<Record<string, unknown>>('GET', '/api/minimax/token-plan')

// Projects
export const listProjects = () => req<Project[]>('GET', '/api/projects')
export const createProject = (data: Partial<Project>) => req<Project>('POST', '/api/projects', data)
export const deleteProject = (id: string) => req<void>('DELETE', `/api/projects/${id}`)
export const renameProject = (id: string, name: string) => req<void>('PATCH', `/api/projects/${id}`, { name })

// Sessions
export const listSessions = (pid: string) => req<Session[]>('GET', `/api/projects/${pid}/sessions`)
export const createSession = (pid: string, data: { name?: string; model?: string; provider?: string }) =>
  req<Session>('POST', `/api/projects/${pid}/sessions`, data)
export const deleteSession = (id: string) => req<void>('DELETE', `/api/sessions/${id}`)

export async function exportSession(id: string): Promise<Blob> {
  const token = getAuthToken()
  const headers: Record<string, string> = {}
  if (token) headers['Authorization'] = `Bearer ${token}`
  const res = await fetch(`/api/sessions/${id}/export`, { headers })
  if (!res.ok) throw new Error(`export failed: ${res.status}`)
  return res.blob()
}

export const importSession = (pid: string, data: unknown) =>
  req<Session>('POST', `/api/projects/${pid}/sessions/import`, data)
export const renameSession = (id: string, name: string) =>
  req<void>('PATCH', `/api/sessions/${id}`, { name })
export const sendTurn = (id: string, message: string, model?: string, provider?: string, effort?: string, thinking?: string, image?: { base64: string; mediaType: string } | null) =>
  req<void>('POST', `/api/sessions/${id}/turn`, { message, model, provider, effort, thinking, image })
export const cancelTurn = (id: string) =>
  req<void>('DELETE', `/api/sessions/${id}/turn`)
export const forkSession = (id: string, name: string, message?: string) =>
  req<Session>('POST', `/api/sessions/${id}/fork`, { name, message })

// Messages
export const listMessages = (id: string, opts?: { limit?: number; beforeSeq?: number }) => {
  const params = new URLSearchParams()
  if (opts?.limit != null) params.set('limit', String(opts.limit))
  if (opts?.beforeSeq != null) params.set('before_seq', String(opts.beforeSeq))
  const qs = params.toString()
  return req<MessagesPage>('GET', `/api/sessions/${id}/messages${qs ? `?${qs}` : ''}`)
}

export const getMessageContext = (messageId: string) => req<Message[]>('GET', `/api/messages/${messageId}/context`)
export const toggleStarMessage = (messageId: string) => req<{ starred: boolean }>('PATCH', `/api/messages/${messageId}/star`)
export const listStarredMessages = (sessionId: string) => req<Message[]>('GET', `/api/sessions/${sessionId}/starred`)
export const listRawEvents = (sessionId: string) => req<any[]>('GET', `/api/sessions/${sessionId}/raw-events`)

// Search
export const search = (q: string) =>
  req<{ results: SearchResult[] }>('GET', `/api/search?q=${encodeURIComponent(q)}`)

// Ollama
export const listOllamaModels = () =>
  req<{ models: { name: string; label: string }[] }>('GET', '/api/ollama/models')

// Config & filesystem
export const getConfig = () => req<{ working_dir: string }>('GET', '/api/config')
export const listDirs = (path: string) =>
  req<{ path: string; parent: string; dirs: string[] }>('GET', `/api/fs/dirs?path=${encodeURIComponent(path)}`)

// Claude accounts: removed in the MiniMax pivot. The /api/accounts/*
// endpoints on the backend return 410 Gone. Nothing to call here.
export const listHomeDirs = (path: string) =>
  req<{ path: string; parent: string; root: string; dirs: string[]; has_credentials: boolean }>('GET', `/api/fs/home-dirs?path=${encodeURIComponent(path)}`)

// File browser — used by the floating side rail in the chat
export interface FileEntry {
  name: string
  path: string
  isDir: boolean
  size: number
}
export const listFiles = (path: string, projectId?: string) => {
  const qs = new URLSearchParams({ path })
  if (projectId) qs.set('project_id', projectId)
  return req<{ path: string; parent: string; root: string; entries: FileEntry[] }>(
    'GET', `/api/fs/list?${qs.toString()}`
  )
}
export const readFile = (path: string, projectId?: string) => {
  const qs = new URLSearchParams({ path })
  if (projectId) qs.set('project_id', projectId)
  return req<{ path: string; content: string; size: number; binary: boolean }>(
    'GET', `/api/fs/file?${qs.toString()}`
  )
}

export interface DailyActivity {
  date: string
  messageCount: number
  sessionCount: number
  toolCallCount: number
}

export interface ModelUsageEntry {
  inputTokens: number
  outputTokens: number
  cacheReadTokens?: number
  cacheWriteTokens?: number
}

// ── Metrics / Usage ─────────────────────────────────────────────────────────

export type UsageRange = '7d' | '30d' | '90d' | 'all'

export interface DailyUsageRow {
  date: string
  model: string
  input_tokens: number
  output_tokens: number
  cache_creation_input_tokens: number
  cache_read_input_tokens: number
  turn_count: number
}

export interface ModelUsageRow {
  model: string
  input_tokens: number
  output_tokens: number
  cache_creation_input_tokens: number
  cache_read_input_tokens: number
  turn_count: number
}

export interface SessionUsageRow {
  session_id: string
  session_name: string
  project_id: string
  model: string
  input_tokens: number
  output_tokens: number
  cache_creation_input_tokens: number
  cache_read_input_tokens: number
  turn_count: number
  duration_minutes: number
  created_at: string
}

export const getUsageDaily = (range: UsageRange) =>
  req<DailyUsageRow[]>('GET', `/api/usage/daily?range=${range}`)

export const getUsageModels = (range: UsageRange) =>
  req<ModelUsageRow[]>('GET', `/api/usage/models?range=${range}`)

export const getUsageSessions = (range: UsageRange, projectId?: string) =>
  req<SessionUsageRow[]>('GET', `/api/usage/sessions?range=${range}${projectId ? `&project_id=${projectId}` : ''}`)

// User Tools
export const listUserTools = () => req<UserTool[]>('GET', '/api/tools')
export const createUserTool = (data: Partial<UserTool>) => req<UserTool>('POST', '/api/tools', data)
export const getUserTool = (id: string) => req<UserTool>('GET', `/api/tools/${id}`)
export const updateUserTool = (id: string, data: Partial<UserTool>) => req<UserTool>('PATCH', `/api/tools/${id}`, data)
export const deleteUserTool = (id: string) => req<void>('DELETE', `/api/tools/${id}`)
export const executeUserTool = (id: string, args: Record<string, unknown>) =>
  req<ToolExecuteResult>('POST', `/api/tools/${id}/execute`, { arguments: args })
export const detectCommand = (transcript: string, accountId: string) =>
  req<CommandDetectResult>('POST', '/api/command-detect', { transcript, account_id: accountId })

// Instances (discovery)
export const getInstances = () =>
  req<{ self: InstanceInfo; peers: InstanceInfo[] }>('GET', '/api/instances')
// Runners and their preconfigured AIs
export const listRunners = () => req<Runner[]>('GET', '/api/runners')
export const listAIProfiles = (runnerId: string) =>
  req<AIProfile[]>('GET', `/api/runners/${runnerId}/ai-profiles`)
export const addAIProfile = (runnerId: string, data: { name: string; claude_config_dir: string }) =>
  req<AIProfile>('POST', `/api/runners/${runnerId}/ai-profiles`, data)
export const refreshAIProfile = (runnerId: string, id: string) =>
  req<AIProfile>('POST', `/api/runners/${runnerId}/ai-profiles/${id}/refresh`)
export const deleteAIProfile = (runnerId: string, id: string) =>
  req<void>('DELETE', `/api/runners/${runnerId}/ai-profiles/${id}`)
export const setProjectAIAPI = (projectId: string, aiapiInstanceId: string) =>
  req<void>('PATCH', `/api/projects/${projectId}/aiapi`, { aiapi_instance_id: aiapiInstanceId })

// Process task manager — Claude CLI processes no longer exist (MiniMax is
// stateless HTTP). The backend returns 410 Gone for these routes; the
// functions are kept so callers compile but they always fail at runtime.
export const listProcesses = () => req<ProcessInfo[]>('GET', '/api/processes')
export const killProcess = (id: string) => req<void>('POST', `/api/processes/${id}/kill`)

// Agents
export const listAgents = (projectId: string) =>
  req<Agent[]>('GET', `/api/projects/${projectId}/agents`)
export const createAgent = (projectId: string, data: Omit<Agent, 'id' | 'project_id' | 'created_at' | 'updated_at'>) =>
  req<Agent>('POST', `/api/projects/${projectId}/agents`, data)
export const getAgent = (id: string) => req<Agent>('GET', `/api/agents/${id}`)
export const updateAgent = (id: string, data: Partial<Omit<Agent, 'id' | 'created_at' | 'updated_at'>>) =>
  req<Agent>('PUT', `/api/agents/${id}`, data)
export const deleteAgent = (id: string) => req<void>('DELETE', `/api/agents/${id}`)
// Tasks run as agent sessions: one chat per kanban card, on the card's agent.
export const openCardSession = (cardId: string) => req<Session>('POST', `/api/cards/${cardId}/session`)
export const listCardComments = (cardId: string) => req<CardComment[]>('GET', `/api/cards/${cardId}/comments`)
export const listAgentSessions = (agentId: string) => req<Session[]>('GET', `/api/agents/${agentId}/sessions`)

// Kanban
export const listKanbanLists = (projectId: string) =>
  req<KanbanList[]>('GET', `/api/projects/${projectId}/kanban/lists`)
export const createKanbanList = (projectId: string, data: { name: string; position: number }) =>
  req<KanbanList>('POST', `/api/projects/${projectId}/kanban/lists`, data)
export const updateKanbanList = (id: string, data: { name?: string; position?: number }) =>
  req<KanbanList>('PUT', `/api/kanban/lists/${id}`, data)
export const deleteKanbanList = (id: string) =>
  req<void>('DELETE', `/api/kanban/lists/${id}`)

export const listKanbanCards = (projectId: string, archived = false) =>
  req<KanbanCard[]>('GET', `/api/projects/${projectId}/kanban/cards${archived ? '?archived=true' : ''}`)
export const createKanbanCard = (projectId: string, data: Partial<KanbanCard>) =>
  req<KanbanCard>('POST', `/api/projects/${projectId}/kanban/cards`, data)
export const updateKanbanCard = (id: string, data: Partial<KanbanCard>) =>
  req<KanbanCard>('PUT', `/api/kanban/cards/${id}`, data)
export const deleteKanbanCard = (id: string) =>
  req<void>('DELETE', `/api/kanban/cards/${id}`)

// ── Checkpoints / Versioning (aiapi:8091) ────────────────────────────────

export const listCheckpoints = (workDir: string) =>
  req<CheckpointListResponse>('GET', `/api/versioning/checkpoints?work_dir=${encodeURIComponent(workDir)}`)

export const getCheckpoint = (id: string, workDir: string) =>
  req<CheckpointWithTree>('GET', `/api/versioning/checkpoints/${encodeURIComponent(id)}?work_dir=${encodeURIComponent(workDir)}`)

export const diffCheckpoints = (from: string, workDir: string, to?: string) => {
  const qs = new URLSearchParams({ work_dir: workDir, from })
  if (to) qs.set('to', to)
  return req<DiffResponse>('GET', `/api/versioning/diff?${qs.toString()}`)
}

export const diffFile = (path: string, workDir: string, from: string, to: string) => {
  const qs = new URLSearchParams({ work_dir: workDir, path, from, to })
  return req<FileDiff>('GET', `/api/versioning/diff/file?${qs.toString()}`)
}

export const revertToCheckpoint = (id: string, workDir: string) =>
  req<{ checkpoint: Checkpoint }>('POST', `/api/versioning/revert/${encodeURIComponent(id)}?work_dir=${encodeURIComponent(workDir)}`)

// ---- Permissions ----

export interface SessionPermissionsResponse {
  session_id: string
  user_file: string
  project_file: string
  session_rules: Array<PermissionRule & { source: string }>
}

export const listSessionPermissions = (sessID: string) =>
  req<SessionPermissionsResponse>('GET', `/api/sessions/${sessID}/permissions`)

export const createSessionPermission = (sessID: string, data: { tool: string; input_regex?: Record<string, string>; scope: PermissionScope }) =>
  req<PermissionRule>('POST', `/api/sessions/${sessID}/permissions`, data)

export const deleteSessionPermission = (sessID: string, ruleID: string) =>
  req<void>('DELETE', `/api/sessions/${sessID}/permissions/${ruleID}`)

export const previewPermission = (workdir: string, tool: string, input?: string) => {
  const qs = new URLSearchParams({ workdir, tool })
  if (input) qs.set('input', input)
  return req<{ decision: string; source: string; path: string; rule: PermissionRule | null }>('GET', `/api/permissions/preview?${qs.toString()}`)
}

export const getPermissionFiles = (workdir: string) =>
  req<{ user_file: string; project_file: string }>('GET', `/api/permissions/files?workdir=${encodeURIComponent(workdir)}`)
