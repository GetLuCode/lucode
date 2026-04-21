import type { Account, Project, Session, MessagesPage, Message, SearchResult, UserTool, CommandDetectResult, ToolExecuteResult, InstanceInfo } from './types'

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

// Projects
export const listProjects = () => req<Project[]>('GET', '/api/projects')
export const createProject = (data: Partial<Project>) => req<Project>('POST', '/api/projects', data)
export const deleteProject = (id: string) => req<void>('DELETE', `/api/projects/${id}`)
export const renameProject = (id: string, name: string) => req<void>('PATCH', `/api/projects/${id}`, { name })

// Sessions
export const listSessions = (pid: string) => req<Session[]>('GET', `/api/projects/${pid}/sessions`)
export const createSession = (pid: string, data: { name?: string; model?: string; provider?: string; account_id?: string }) =>
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

// Accounts
export const listAccounts = () => req<Account[]>('GET', '/api/accounts')
export const createAccount = (data: {
  name: string
  access_token?: string
  refresh_token?: string
  expires_at?: number
  claude_config_dir?: string
}) => req<Account>('POST', '/api/accounts', data)
export const deleteAccount = (id: string) => req<void>('DELETE', `/api/accounts/${id}`)
export const updateAccountCredentials = (id: string, data: {
  access_token: string
  refresh_token: string
  expires_at: number
}) => req<Account>('PUT', `/api/accounts/${id}/credentials`, data)
export const listHomeDirs = (path: string) =>
  req<{ path: string; parent: string; root: string; dirs: string[]; has_credentials: boolean }>('GET', `/api/fs/home-dirs?path=${encodeURIComponent(path)}`)

export interface AccountProfile {
  account: {
    uuid: string
    full_name: string
    display_name: string
    email: string
    has_claude_max: boolean
    has_claude_pro: boolean
    created_at: string
  }
  organization: {
    uuid: string
    name: string
    organization_type: string
    billing_type: string
    rate_limit_tier: string
    has_extra_usage_enabled: boolean
    subscription_status: string
    subscription_created_at: string
  }
  application: {
    uuid: string
    name: string
    slug: string
  }
}

export interface AccountUsage {
  five_hour: { utilization: number; resets_at: string } | null
  seven_day: { utilization: number; resets_at: string } | null
  seven_day_oauth_apps: { utilization: number; resets_at: string } | null
  seven_day_opus: { utilization: number; resets_at: string } | null
  seven_day_sonnet: { utilization: number; resets_at: string } | null
  seven_day_cowork: { utilization: number; resets_at: string } | null
  extra_usage: {
    is_enabled: boolean
    monthly_limit: number
    used_credits: number
    utilization: number
  } | null
}

export const getAccountProfile = (id: string) => req<AccountProfile>('GET', `/api/accounts/${id}/profile`)
export const getAccountUsage = (id: string, refresh = false) =>
  req<AccountUsage>('GET', `/api/accounts/${id}/usage${refresh ? '?refresh=true' : ''}`)

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

export interface AccountStats {
  version: number
  lastComputedDate: string
  dailyActivity: DailyActivity[]
  totalSessions: number
  totalMessages: number
  longestSession: number
  firstSessionDate?: string
  modelUsage?: Record<string, ModelUsageEntry>
}

export const getAccountStats = (id: string) => req<AccountStats>('GET', `/api/accounts/${id}/stats`)

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
export const setProjectAIAPI = (projectId: string, aiapiInstanceId: string) =>
  req<void>('PATCH', `/api/projects/${projectId}/aiapi`, { aiapi_instance_id: aiapiInstanceId })
