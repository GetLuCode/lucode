export interface Account {
  id: string
  name: string
  claude_config_dir?: string
  has_token: boolean
  expires_at?: number
  created_at: string
}

export interface Project {
  id: string
  name: string
  work_dirs: string[]
  claude_config_dir: string
  default_model: string
  aiapi_instance_id?: string
  created_at: string
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
  created_at: string
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
  | { type: 'session_status'; session_id: string; status: string }
  | { type: 'stream_event'; session_id: string; event: unknown; partial?: boolean }
  | { type: 'user_message'; session_id: string; id: string; content: string; seq: number; created_at: string }
  | { type: 'tool_execution'; session_id: string; tool_name: string; tool_use_id: string; tool_input: unknown; timestamp: string; message_id: string }
  | { type: 'tool_execution_complete'; session_id: string; tool_use_id: string; tool_response: unknown; message_id: string }
  | { type: 'hook_pending'; session_id: string; tool_use_id: string; tool_name: string; tool_input: unknown; created_at: string }
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
  created_at: string
  updated_at: string
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
