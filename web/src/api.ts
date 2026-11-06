import type { ActiveFlowRun, Flow, FlowApproval, FlowBlueprint, FlowGraph, FlowNodeType, FlowProblem, FlowRun, FlowStep, Alert, AIProfile, Runner, ClaudeCommand, RunnerVersion, CardComment, Project, Session, MessagesPage, Message, SearchResult, UserTool, CommandDetectResult, ToolExecuteResult, InstanceInfo, ProcessInfo, ClaudeProcess, AIModeConversation, OrchestratorChat, Agent, KanbanList, KanbanCard, CheckpointListResponse, CheckpointWithTree, DiffResponse, FileDiff, Checkpoint, PermissionRule, PermissionScope } from './types'
import { useAuthStore } from './store/auth'

const BASE = process.env.VITE_API_BASE ?? ''

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
export const updateProjectEnvironment = (id: string, data: { runner_id: string; ai_profile_id: string; env_dirs?: Record<string, string[]> }) =>
  req<void>('PATCH', `/api/projects/${id}`, data)
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
// The project's orchestrator chat (created on first use).
export const getOrchestrator = (pid: string) => req<Session>('GET', `/api/projects/${pid}/orchestrator`)
// Closes the current orchestrator conversation and starts an empty one.
export const newOrchestratorSession = (pid: string) => req<Session>('POST', `/api/projects/${pid}/orchestrator/new`)
// History of the local Google AI Mode model: its conversations by url, and bringing one back to work on.
export const listAIModeConversations = (pid: string) => req<AIModeConversation[]>('GET', `/api/projects/${pid}/aimode/conversations`)
export const continueAIModeConversation = (id: string) => req<Session>('POST', `/api/aimode/conversations/${id}/continue`)
// History of the Supervisor conversations of a project: the ones claude can pick up again.
export const listSupervisorChats = (pid: string) =>
  req<OrchestratorChat[]>('GET', `/api/orchestrator/chats?project=${encodeURIComponent(pid)}&mode=supervisor`)
export const updateOrchestratorChat = (id: string, data: { name?: string; project_id?: string }) =>
  req<Session>('PATCH', `/api/orchestrator/chats/${id}`, data)
export const openOrchestratorChat = (id: string) => req<Session>('POST', `/api/orchestrator/chats/${id}/open`)
export const sendOrchestratorTurn =(id: string, message: string, env: { runnerId: string; aiProfileId: string; model?: string }, image?: { base64: string; mediaType: string } | null) =>
  req<void>('POST', `/api/sessions/${id}/turn`, { message, runner_id: env.runnerId, ai_profile_id: env.aiProfileId, model: env.model, ...(image ? { image } : {}) })
// Voice output (Fish Audio through the voice worker). The key is write-only.
export interface TTSSettings { configured: boolean; model: string; voice_id: string; speed: number }
export const getTTSSettings = () => req<TTSSettings>('GET', '/api/tts/settings')
export const saveTTSSettings = (data: { api_key?: string; clear_key?: boolean; model: string; voice_id: string; speed: number }) =>
  req<TTSSettings>('PUT', '/api/tts/settings', data)
// Orchard Run, the second speech recognition engine (cloud, push to talk). The key is write-only.
export interface OrchardSettings { configured: boolean; language: string; prompt: string }
export const getOrchardSettings = () => req<OrchardSettings>('GET', '/api/stt/orchard/settings')
export const saveOrchardSettings = (data: { api_key?: string; clear_key?: boolean; language: string; prompt: string }) =>
  req<OrchardSettings>('PUT', '/api/stt/orchard/settings', data)
// Sends one recording (WAV) and gets its text.
export async function orchardTranscribe(wav: Blob, signal?: AbortSignal): Promise<string> {
  const headers: Record<string, string> = { 'Content-Type': 'audio/wav' }
  const token = getAuthToken()
  if (token) headers['Authorization'] = `Bearer ${token}`
  if (activeRemoteId) headers['X-Lucode-Remote'] = activeRemoteId
  const res = await fetch(`${BASE}/api/stt/orchard/transcribe`, { method: 'POST', headers, body: wav, signal })
  if (!res.ok) {
    if (res.status === 401) useAuthStore.getState().clearAuth()
    throw new Error((await res.text()).trim() || `Orchard failed (${res.status})`)
  }
  return ((await res.json()) as { text: string }).text
}
// Speech recognition model of the shared voice worker. `selected` is the choice kept by lucode;
// `worker` is what runs right now (null: nothing answers, it starts when somebody needs it).
export interface SttModel { id: string; label: string; device: 'gpu' | 'cpu'; memory: string; recommended?: boolean }
export interface SttWorkerInfo { model: string; ready: boolean; listening: boolean }
export interface SttModels { models: SttModel[]; selected: string; worker: SttWorkerInfo | null }
export const getSttModels = () => req<SttModels>('GET', '/api/stt/models')
// Saves the choice and replaces a running worker that uses another model.
export const setSttModel = (model: string) =>
  req<{ selected: string; worker: SttWorkerInfo | null }>('POST', '/api/stt/model', { model })
// Raw PCM16 mono audio stream of the text (X-Sample-Rate says the rate).
export const speakRequest = (text: string, signal: AbortSignal) => {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  const token = getAuthToken()
  if (token) headers['Authorization'] = `Bearer ${token}`
  return fetch(`${BASE}/api/tts`, { method: 'POST', headers, body: JSON.stringify({ text }), signal })
}
// The Vosk Spanish model (.tar.gz) for the hotword detector that runs in the browser. It is fetched here,
// with the login token, because vosk-browser's worker cannot send headers; the result is a blob: URL.
export const fetchVoskModel = async (): Promise<string> => {
  const headers: Record<string, string> = {}
  const token = getAuthToken()
  if (token) headers['Authorization'] = `Bearer ${token}`
  const res = await fetch(`${BASE}/api/stt/vosk-model.tar.gz`, { headers })
  if (!res.ok) throw new Error(`hotword model: ${(await res.text()).trim() || res.status}`)
  return URL.createObjectURL(await res.blob())
}
// The persistent claude process of a session: is it alive, and close it (the next message starts a new one).
// What the session's task branch changes against its base; computed by the runner that holds the worktree.
export interface DiffStat {
  base: string
  branch: string
  added: number     // lines added
  removed: number   // lines removed
  created: number   // files created
  modified: number
  deleted: number   // files deleted
  error?: string
}
export const getSessionDiffStat = (id: string) => req<DiffStat>('GET', `/api/sessions/${id}/diffstat`)
export const getSessionProcess = (id: string) => req<ClaudeProcess>('GET', `/api/sessions/${id}/process`)
export const closeSessionProcess = (id: string) => req<void>('DELETE', `/api/sessions/${id}/process`)
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
// runnerId browses the directories of a peer runner instead of this machine.
export const listDirs = (path: string, runnerId?: string) =>
  req<{ path: string; parent: string; dirs: string[] }>(
    'GET',
    runnerId
      ? `/api/runners/${runnerId}/dirs?path=${encodeURIComponent(path)}`
      : `/api/fs/dirs?path=${encodeURIComponent(path)}`,
  )

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
// An exact window (ISO instants, `to` excluded) instead of a preset range.
export interface UsageSpan { from: string; to: string }
const windowQS = (w: UsageRange | UsageSpan) =>
  typeof w === 'string' ? `range=${w}` : `from=${encodeURIComponent(w.from)}&to=${encodeURIComponent(w.to)}`

export interface DailyUsageRow {
  date: string
  model: string
  input_tokens: number
  output_tokens: number
  cache_creation_input_tokens: number
  cache_read_input_tokens: number
  cache_creation_1h_input_tokens?: number // of the written ones, the 1 hour cache
  turn_count: number
}

export interface ModelUsageRow {
  model: string
  input_tokens: number
  output_tokens: number
  cache_creation_input_tokens: number
  cache_read_input_tokens: number
  cache_creation_1h_input_tokens?: number
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
  cache_creation_1h_input_tokens?: number
  turn_count: number
  duration_minutes: number
  created_at: string
}

// hourly: one row per (UTC) hour, `date` being "YYYY-MM-DDTHH", instead of per day
export const getUsageDaily = (range: UsageRange | UsageSpan, hourly = false) =>
  req<DailyUsageRow[]>('GET', `/api/usage/daily?${windowQS(range)}${hourly ? '&granularity=hour' : ''}`)

export const getUsageModels = (range: UsageRange | UsageSpan) =>
  req<ModelUsageRow[]>('GET', `/api/usage/models?${windowQS(range)}`)

export const getUsageSessions = (range: UsageRange | UsageSpan, projectId?: string) =>
  req<SessionUsageRow[]>('GET', `/api/usage/sessions?${windowQS(range)}${projectId ? `&project_id=${projectId}` : ''}`)

// The tokens of a session's first turn (what its initial context really wrote to / read from
// the cache). Rejects with a 404 until the session has one.
export interface FirstTurnUsage {
  // true: the session's very first call to the model (exactly what its initial context billed).
  // false: an older session, only with the turn totals, which hold the LAST call of the turn.
  exact?: boolean
  cache_creation_1h_tokens?: number // of the written tokens, the ones billed at the 1h price
  model: string
  input_tokens: number
  output_tokens: number
  cache_creation_input_tokens: number
  cache_read_input_tokens: number
}
export const getFirstTurnUsage = (sessionId: string) =>
  req<FirstTurnUsage>('GET', `/api/sessions/${sessionId}/first-turn-usage`)

// What one tool used in one model, in tokens by price class (see internal/db/toolcost.go).
export interface ToolCostRow {
  tool: string
  model: string
  calls: number
  errors: number
  result_tokens: number  // added to the context by its results
  unmeasured: number     // calls whose result was never measured (the last call of a turn)
  output_tokens: number  // the model's output to ask for them
  written_1h: number     // result tokens written to the cache with a 1h TTL
  written_5m: number
  uncached: number
  carried_read: number   // result tokens x the later calls that re-read them
}
export const getUsageTools = (range: UsageRange | UsageSpan, projectId?: string) =>
  req<ToolCostRow[]>('GET', `/api/usage/tools?${windowQS(range)}${projectId ? `&project_id=${projectId}` : ''}`)

// What each tool cost in one session (empty until the session has reported its calls).
export const getSessionToolCosts = (sessionId: string) =>
  req<ToolCostRow[]>('GET', `/api/sessions/${sessionId}/tool-costs`)

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
// What a runner launched, newest first, and which build it is. The hub only relays them:
// the runner is the one that knows.
export const listRunnerCommands = (runnerId: string, limit = 100) =>
  req<ClaudeCommand[]>('GET', `/api/runners/${runnerId}/commands?limit=${limit}`)
export const getRunnerVersion = (runnerId: string) =>
  req<RunnerVersion>('GET', `/api/runners/${runnerId}/version`)
// The command a session's claude was last started with (reported by the runner).
export const getSessionCommand = (sessionId: string) =>
  req<ClaudeCommand>('GET', `/api/sessions/${sessionId}/command`)
export const listAIProfiles = (runnerId: string) =>
  req<AIProfile[]>('GET', `/api/runners/${runnerId}/ai-profiles`)
export const addAIProfile = (runnerId: string, data: { name: string; claude_config_dir: string }) =>
  req<AIProfile>('POST', `/api/runners/${runnerId}/ai-profiles`, data)
export const refreshAIProfile = (runnerId: string, id: string) =>
  req<AIProfile>('POST', `/api/runners/${runnerId}/ai-profiles/${id}/refresh`)
export const deleteAIProfile = (runnerId: string, id: string) =>
  req<void>('DELETE', `/api/runners/${runnerId}/ai-profiles/${id}`)
// Plan usage windows of an AI's account (Anthropic /api/oauth/usage). utilization is 0-100.
export interface UsageWindow { utilization: number | null; resets_at: string | null }
export interface AIProfileUsage { five_hour?: UsageWindow | null; seven_day?: UsageWindow | null; seven_day_opus?: UsageWindow | null; seven_day_sonnet?: UsageWindow | null }
// The AI picked in the usage container: orchestrator/agents without an AI of their own use it.
export interface ActiveAI { runner_id: string; ai_profile_id: string }
export const getActiveAI = () => req<ActiveAI>('GET', '/api/active-ai')
export const setActiveAI = (a: ActiveAI) => req<ActiveAI>('PUT', '/api/active-ai', a)
export const getAIProfileUsage =(runnerId: string, id: string) =>
  req<AIProfileUsage>('GET', `/api/runners/${runnerId}/ai-profiles/${id}/usage`)
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
export interface CardImage { name: string; media_type: string; data: string } // data is base64
export const listCardImages = (cardId: string) => req<CardImage[]>('GET', `/api/kanban/cards/${cardId}/images`)
export const addCardImage = (cardId: string, media_type: string, data: string) =>
  req<CardImage>('POST', `/api/kanban/cards/${cardId}/images`, { media_type, data })
export const deleteCardImage = (cardId: string, name: string) =>
  req<void>('DELETE', `/api/kanban/cards/${cardId}/images/${name}`)
export const resetKanbanCard = (id: string) =>
  req<void>('POST', `/api/kanban/cards/${id}/reset`)

// Review: the git branch an agent left in a worktree, waiting to be merged.
export interface ReviewStatus {
  branch: string
  base: string              // the branch checked out in the main checkout
  worktree_path: string
  repo_path: string
  ahead: number             // commits not in base yet
  files: string[]           // "M path" per changed file
  dirty: boolean            // uncommitted changes in the worktree
  conflicts: boolean
  conflict_files: string[]
  merged: boolean
  error?: string
}
export const getCardReview = (id: string) => req<ReviewStatus>('GET', `/api/kanban/cards/${id}/review`)

// Git tab: the runner that holds the repository answers, the hub relays (see components/Git).
export const getGitTargets = <T,>(projectId: string) => req<T>('GET', `/api/projects/${projectId}/git/targets`)
export const projectGit = <T,>(projectId: string, op: string, body: Record<string, unknown>) =>
  req<T>('POST', `/api/projects/${projectId}/git/${op}`, body)
export const mergeCardReview = (id: string) =>
  req<{ card: KanbanCard; warnings: string[] }>('POST', `/api/kanban/cards/${id}/review/merge`)
export const resolveCardReview = (id: string) =>
  req<{ card: KanbanCard }>('POST', `/api/kanban/cards/${id}/review/resolve`)

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

// Things to clean up by hand in the project's repositories (merged worktrees left behind).
export const listProjectAlerts = (projectId: string) => req<Alert[]>('GET', `/api/projects/${projectId}/alerts`)

// Flows
export const listFlowNodeTypes = () => req<FlowNodeType[]>('GET', '/api/flow-node-types')
export const getCardMachine = () => req<Record<string, unknown>>('GET', '/api/card-machine')
export const listFlows = (projectId: string) => req<Flow[]>('GET', `/api/projects/${projectId}/flows`)
export const createFlow = (projectId: string, name: string, blueprint?: string) => req<Flow>('POST', `/api/projects/${projectId}/flows`, { name, blueprint })
export const listFlowBlueprints = () => req<FlowBlueprint[]>('GET', '/api/flow-blueprints')
export const getFlow = (id: string) => req<Flow>('GET', `/api/flows/${id}`)
export const saveFlow = (id: string, data: { name: string; draft: FlowGraph }) => req<Flow>('PUT', `/api/flows/${id}`, data)
export const deleteFlow = (id: string) => req<void>('DELETE', `/api/flows/${id}`)
export const validateFlow = (id: string) => req<{ problems: FlowProblem[] }>('POST', `/api/flows/${id}/validate`)
export const publishFlow = (id: string) => req<Flow>('POST', `/api/flows/${id}/publish`)
export const setFlowEnabled = (id: string, enabled: boolean) => req<Flow>('PUT', `/api/flows/${id}/enabled`, { enabled })
export const runFlow = (id: string, cardId?: string) => req<FlowRun>('POST', `/api/flows/${id}/run`, { card_id: cardId ?? '' })
export const listFlowRuns = (id: string) => req<FlowRun[]>('GET', `/api/flows/${id}/runs`)
export const getFlowRun = (id: string) => req<{ run: FlowRun; steps: FlowStep[]; graph: FlowGraph }>('GET', `/api/flow-runs/${id}`)
export const cancelFlowRun = (id: string) => req<void>('POST', `/api/flow-runs/${id}/cancel`)
export const listActiveFlowRuns = (projectId: string) => req<ActiveFlowRun[]>('GET', `/api/projects/${projectId}/flow-runs/active`)
export const getFlowsPaused = (projectId: string) => req<{ paused: boolean }>('GET', `/api/projects/${projectId}/flows/paused`)
export const setFlowsPaused = (projectId: string, paused: boolean) => req<{ paused: boolean }>('PUT', `/api/projects/${projectId}/flows/paused`, { paused })
export const listFlowApprovals = (projectId: string) => req<FlowApproval[]>('GET', `/api/projects/${projectId}/flow-approvals`)
export const decideFlowApproval = (seq: number, decision: 'approved' | 'rejected', note = '') => req<void>('POST', `/api/flow-steps/${seq}/decision`, { decision, note })
export const getFlowAISettings = () => req<{ configured: boolean; source: string }>('GET', '/api/flow-ai-settings')
export const saveFlowAIKey = (apiKey: string) => req<{ configured: boolean; source: string }>('PUT', '/api/flow-ai-settings', { api_key: apiKey })
export const clearFlowAIKey = () => req<{ configured: boolean; source: string }>('PUT', '/api/flow-ai-settings', { clear: true })
export const getFlowPush = () => req<{ public_key: string }>('GET', '/api/flow-push')
export const savePushSubscription = (sub: { endpoint: string; keys: { p256dh: string; auth: string } }) => req<void>('PUT', '/api/flow-push/subscription', sub)
export const removePushSubscription = (endpoint: string) => req<void>('DELETE', '/api/flow-push/subscription', { endpoint })
export const testPushSubscription = (endpoint: string) => req<void>('POST', '/api/flow-push/test', { endpoint })
