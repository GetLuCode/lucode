export interface Project {
  id: string
  name: string
  work_dirs: string[]
  claude_config_dir: string
  default_model: string
  created_at: string
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
  role: 'user' | 'assistant' | 'tool_use' | 'tool_result' | 'system' | 'thinking' | 'raw'
  content: string
  seq: number
  created_at: string
  usage?: MessageUsage
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
}

// WebSocket events from backend
export type WSEvent =
  | { type: 'session_status'; session_id: string; status: string }
  | { type: 'stream_event'; session_id: string; event: unknown; partial?: boolean }
  | { type: 'hook_pending'; session_id: string; tool_use_id: string; tool_name: string; tool_input: unknown }
  | { type: 'hook_resolved'; session_id: string; tool_use_id: string; decision: string }
  | { type: 'turn_complete'; session_id: string; result_subtype: string; result_text: string }
  | { type: 'session_updated'; session: Session }
  | { type: 'error'; session_id: string; message: string }
  | { type: 'info'; session_id: string; message: string }

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
