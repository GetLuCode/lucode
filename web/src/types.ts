// Note: the Claude-accounts subsystem was removed in the MiniMax pivot.
// Account-related types and fields have been dropped; existing rows in the
// DB still hold legacy data but the UI no longer reads or writes them.

export interface Project {
  id: string
  name: string
  work_dirs: string[]
  claude_config_dir?: string
  default_model: string
  aiapi_instance_id?: string
  runner_id?: string
  ai_profile_id?: string
  env_dirs?: Record<string, string[]>
  created_at: string
}

// A lucode instance that can run projects (this one or a peer found over NATS).
export interface Runner {
  id: string
  hostname: string
  os: string
  self: boolean
}

// A preconfigured AI of a runner: a claude_config_dir plus the account it belongs to.
export interface AIProfile {
  id: string
  name: string
  claude_config_dir: string
  email: string
  display_name: string
  organization: string
  plan: string
  error: string
  checked_at: string
}

export interface InstanceInfo {
  id: string
  type: 'lucode' | 'aiapi'
  hostname: string
  os: string
  started_at: string
  extra?: Record<string, unknown>
}

export interface Session {
  id: string
  project_id: string
  name: string
  model: string
  status: 'idle' | 'running' | 'waiting_hook' | 'error'
  parent_id?: string
  message_count: number
  created_at: string
  last_active: string
  context_usage?: string  // JSON-encoded context usage, persisted per session
  card_id?: string        // the task (kanban card) this session belongs to
  agent_id?: string       // the agent running it
}

export interface MessageUsage {
  input_tokens: number
  output_tokens: number
  cache_creation_input_tokens?: number
  cache_read_input_tokens?: number
}

export interface Message {
  id: string
  session_id: string
  role: 'user' | 'assistant' | 'tool_use' | 'tool_result' | 'system' | 'thinking' | 'raw' | 'hook' | 'tool_execution'
  content: string
  seq: number
  created_at: string
  usage?: MessageUsage
  starred?: boolean
}

export interface MessagesPage {
  messages: Message[]
  has_more: boolean
  oldest_seq: number
}

export interface PendingHook {
  session_id: string
  tool_use_id: string
  tool_name: string
  tool_input: unknown
  tool_input_schema?: ToolInputField[] | null
  created_at: string
}

export interface ToolInputField {
  name: string
  description?: string
  type: 'string' | 'number' | 'boolean' | 'array' | 'object'
  required?: boolean
}

export type PermissionScope = 'allow' | 'deny' | 'ask'

export interface PermissionRule {
  id: string
  tool: string
  input_regex?: Record<string, string>
  scope: PermissionScope
  source?: 'user' | 'project' | 'session' | string
}

export interface SearchResult {
  type: 'project' | 'session' | 'message'
  id: string
  project_id: string
  session_id: string
  title: string
  snippet: string
  seq: number
}

// Context window usage breakdown
export interface ContextCategory {
  name: string
  tokens: number
  color: string
}

export interface ContextUsage {
  categories: ContextCategory[]
  totalTokens: number
  maxTokens: number
  percentage: number
  model: string
  session_id: string
}

// WebSocket events from backend
export type WSEvent =
  | { type: 'session_status'; session_id: string; status: string; error?: string }
  | { type: 'stream_event'; session_id: string; event: unknown; partial?: boolean }
  | { type: 'user_message'; session_id: string; id: string; content: string; seq: number; created_at: string }
  | { type: 'tool_execution'; session_id: string; tool_name: string; tool_use_id: string; tool_input: unknown; timestamp: string; message_id: string }
  | { type: 'tool_execution_complete'; session_id: string; tool_use_id: string; tool_response: unknown; message_id: string }
  | { type: 'task_updated'; session_id: string; project_id: string; card_id: string }
  | { type: 'hook_pending'; session_id: string; tool_use_id: string; tool_name: string; tool_input: unknown; tool_input_schema?: ToolInputField[] | null; created_at: string }
  | { type: 'hook_resolved'; session_id: string; tool_use_id: string; decision: string }
  | { type: 'turn_complete'; session_id: string; result_subtype: string; result_text: string }
  | { type: 'session_updated'; session: Session }
  | { type: 'error'; session_id: string; message: string }
  | { type: 'info'; session_id: string; message: string }
  | { type: 'context_usage'; session_id: string; categories: ContextCategory[]; totalTokens: number; maxTokens: number; percentage: number; model: string }
  | { type: 'turn_usage'; session_id: string; input_tokens: number; output_tokens: number; cache_creation_input_tokens: number; cache_read_input_tokens: number; model: string }
  | { type: 'message_ready'; session_id: string; message: { content: string; role: string } }

// Structured assistant message content
export interface AssistantMessage {
  type: 'assistant'
  message: {
    role: 'assistant'
    content: AssistantContent[]
  }
}

export type AssistantContent =
  | { type: 'text'; text: string }
  | { type: 'tool_use'; id: string; name: string; input: unknown }

export interface ToolResultMessage {
  type: 'user'
  message: {
    role: 'user'
    content: ToolResultContent[]
  }
}

export type ToolResultContent = {
  type: 'tool_result'
  tool_use_id: string
  content: string | { type: string; text: string }[]
}

// ---- User-defined tools (voice command detection) ----

export interface UserTool {
  id: string
  name: string
  description: string
  input_schema: string // JSON string
  code: string
  execution_env: 'browser' | 'orchestrator'
  created_at: string
  updated_at: string
}

export interface CommandDetectResult {
  tool_call: {
    tool_name: string
    tool_id: string
    arguments: Record<string, unknown>
  } | null
  reason?: string
}

export interface ToolExecuteResult {
  success: boolean
  result?: unknown
  error?: string
}

export interface KanbanList {
  id: string
  project_id: string
  name: string
  position: number
  created_at: string
}

export interface KanbanCard {
  id: string
  list_id: string
  project_id: string
  title: string
  description: string
  card_type: string
  tags: string[]
  agent_id: string
  depends_on: string[]
  start_date: string
  end_date: string
  position: number
  archived: boolean
  parent_id: string
  created_at: string
  updated_at: string
}

export interface Agent {
  id: string
  project_id: string
  name: string
  model: string
  account_id: string
  personality: string
  tools: string[]
  executor: 'aiapi' | 'claude'  // how the agent runs: aiapi service or the claude -p CLI
  scope: 'project' | 'global'   // global agents are available in every project
  lucode_tools: string[]        // lucode tools the agent has (task_*, agent_list, send_to_agent, task_info)
  persistent: boolean           // keep the claude -p process alive between messages
  reports_to: string            // id of the supervising agent ('' = none)
  runner_id: string             // runner the agent works on ('' = the project's)
  ai_profile_id: string         // preconfigured AI of that runner ('' = the project's)
  created_at: string
  updated_at: string
}

// A note an agent posted on a task (progress, completion summary or blocker).
export interface CardComment {
  id: string
  card_id: string
  session_id?: string
  author: string
  kind: 'comment' | 'done' | 'blocked' | 'error'
  body: string
  created_at: string
}

export interface RawEventEntry {
  id: string
  turnIndex: number
  timestamp: string
  event: WSEvent
}

export interface ProcessInfo {
  id: string
  started_at: string  // RFC3339
  pid: number
  model: string
  ram_bytes: number   // 0 = unavailable (non-Linux)
}

// ── Checkpoints ─────────────────────────────────────────────────────────────

export interface Checkpoint {
  id: string
  parent: string
  tree: string
  timestamp: string
  turn_id?: string
  summary: string
  ops?: FSOpsForUI[]
  kind?: 'turn' | 'revert' | string
  pruned?: boolean
}

export interface FSOpsForUI {
  op: 'create' | 'update' | 'delete' | 'rename' | 'copy'
  path?: string
  from?: string
  to?: string
  old_string?: string
  new_string?: string
  replace_all?: boolean
  occurrence_index?: number
}

export type ChangeKind = 'added' | 'removed' | 'modified'

export interface CheckpointChange {
  path: string
  kind: ChangeKind
  from_hash?: string
  to_hash?: string
}

export interface FileDiff {
  path: string
  from_hash?: string
  to_hash?: string
  kind: ChangeKind
  unified_diff: string
  is_binary: boolean
  from_content?: string
  to_content?: string
  from_lines: number
  to_lines: number
}

export interface CheckpointTree {
  entries: { path: string; hash: string }[]
}

export interface CheckpointWithTree {
  checkpoint: Checkpoint
  tree: CheckpointTree
}

export interface CheckpointListResponse {
  work_dir: string
  head: string
  checkpoints: Checkpoint[]
}

export interface DiffResponse {
  from: string
  to: string
  changes: CheckpointChange[]
}
