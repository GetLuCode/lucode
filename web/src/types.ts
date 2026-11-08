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
  last_used_at?: string // latest activity of its sessions or tasks; zero date when none
}

// A lucode instance that can run projects (this one or a peer found over NATS).
export interface Runner {
  id: string
  hostname: string
  os: string
  self: boolean
  version?: string      // short git commit it was built from ("+dirty" = uncommitted changes)
  started_at?: string   // RFC3339
  runner_only?: boolean // lucode-runner: it only executes; the hub never does
}

// A claude command line a runner launched (or failed to launch).
export interface ClaudeCommand {
  id: string
  session_id: string
  runner_id?: string
  at: string            // RFC3339
  exe: string
  args: string[]
  cwd: string
  pid: number
  error?: string
}

// GET /api/version of a runner.
export interface RunnerVersion {
  commit?: string
  short: string
  commit_time?: string
  dirty: boolean
  built_at?: string
  started_at: string
  go: string
  os: string
  arch: string
  role?: string
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
  kind?: 'orchestrator' | 'plugin' | '' // the project's main chat (right-hand panel), or a chat held by a plugin's page
  worktree?: string       // git worktree the agent works in (from its transcript)
  worktree_removed?: boolean // merging its task deleted that worktree: the chat is closed
  branch?: string         // its branch
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
  | { type: 'tool_execution'; session_id: string; tool_name: string; tool_use_id: string; tool_input: unknown; timestamp: string; message_id: string; decision?: string; decided_by?: string; reason?: string }
  | { type: 'tool_execution_complete'; session_id: string; tool_use_id: string; tool_response: unknown; message_id: string }
  | { type: 'task_updated'; session_id: string; project_id: string; card_id: string }
  | { type: 'flow_updated'; project_id: string; flow_id: string; run_id: string; status: string }
  | { type: 'flow_draft_updated'; project_id: string; flow_id: string; agent: string }
  | { type: 'hook_pending'; session_id: string; tool_use_id: string; tool_name: string; tool_input: unknown; tool_input_schema?: ToolInputField[] | null; created_at: string }
  | { type: 'hook_resolved'; session_id: string; tool_use_id: string; decision: string }
  | { type: 'turn_complete'; session_id: string; result_subtype: string; result_text: string }
  | { type: 'session_updated'; session: Session }
  | { type: 'error'; session_id: string; message: string }
  | { type: 'info'; session_id: string; message: string }
  | { type: 'progress'; session_id: string; text: string }
  | { type: 'claude_process'; session_id: string; alive: boolean; pid?: number; runner?: string }
  | ({ type: 'claude_workspace'; session_id: string } & ClaudeWorkspace)
  | { type: 'context_usage'; session_id: string; categories: ContextCategory[]; totalTokens: number; maxTokens: number; percentage: number; model: string }
  | { type: 'turn_usage'; session_id: string; input_tokens: number; output_tokens: number; cache_creation_input_tokens: number; cache_read_input_tokens: number; cache_creation_1h_input_tokens?: number; model: string }
  | { type: 'message_ready'; session_id: string; message: { content: string; role: string } }
  | { type: 'peers_changed' }
  | { type: 'plugins_changed' }
  // a webhook reached a plugin: no body, the plugin's page asks the plugin to show it
  | { type: 'plugin_webhook'; pluginId: string; provider: string; endpoint: string; projectId: string; receivedAt: number }

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
  kind?: string  // workflow role: backlog, todo, inprogress, review, done, blocked (custom lists have none)
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
  started_at: string   // real start (entered In Progress), RFC3339; empty = never
  finished_at: string  // real end (entered Done), RFC3339; empty = not finished
  position: number
  archived: boolean
  parent_id: string
  last_error?: string  // why the task's session is in error (newest error note)
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
  auto_mode: boolean            // claude --permission-mode auto: what rules and flows leave open goes to claude, not to a person
  worktree: boolean             // claude --worktree: work in its own git worktree
  worktree_base_ref: 'head' | 'fresh' // worktree.baseRef: branch off the current HEAD (default) or a fresh remote default branch
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

// Whether a session keeps a persistent claude process alive between turns.
export interface ClaudeProcess {
  alive: boolean
  pid?: number     // local processes only
  runner?: string  // the runner holding it, when it is not this instance
  workspace?: ClaudeWorkspace
}

// Where claude works, as the machine that runs it reported when the process started.
export interface ClaudeWorkspace {
  cwd: string
  path?: string      // git root of the cwd (the worktree itself, when it is one)
  branch?: string
  worktree?: boolean // a linked git worktree, not the main checkout
  repo?: string      // the main checkout a worktree belongs to
}

// A Google AI Mode conversation of the local model: the chat it belongs to and the url that
// reopens it (it works with the browser profile lucode keeps for it).
export interface OrchestratorChat {
  id: string
  name: string // '' until the user renames it
  preview: string // the first thing the user wrote
  project_id: string
  project_name: string
  model: string
  message_count: number
  last_active: string
  current: boolean // the chat its project's panel shows now
}

// Something lucode noticed that the user may want to clean up by hand (the bell in the header).
export interface Alert {
  id: string
  kind: 'stale_worktree'
  title: string
  detail: string
  command: string // what to run to clean it up
}

// Flows: automations of a project. The hub owns the node types, validates and runs them.
export interface FlowNode { id: string; type: string; x: number; y: number; config?: Record<string, unknown> }
export interface FlowEdge { from: string; port: string; to: string }
export interface FlowGraph { nodes: FlowNode[]; edges: FlowEdge[] }
export interface FlowNodeField { name: string; label: string; kind: 'text' | 'template' | 'expr' | 'number' | 'select' | 'agent' | 'list' | 'bool' | 'lines'; required?: boolean; options?: string[]; default?: unknown; help?: string; min?: number; port_label?: string }
export interface FlowNodeType { type: string; title: string; category: 'trigger' | 'control' | 'action'; description: string; ports: string[] | null; fields: FlowNodeField[] | null; plugin_id?: string; plugin_name?: string; icon?: string; target?: PluginTarget; plugin_disabled?: boolean }
export interface Flow { id: string; project_id: string; name: string; draft: FlowGraph; published_version: number; enabled: boolean; created_at: string; updated_at: string; draft_agent?: string; plugin_id?: string; plugin_blueprint?: string; locked?: boolean; forked_from?: string }
export interface FlowProblem { node_id?: string; message: string }
export interface FlowRun { id: string; flow_id: string; version: number; project_id: string; card_id: string; event_id: number; event: unknown; depth: number; status: string; error: string; started_at: string; finished_at: string }
export interface FlowStep { seq: number; run_id: string; node_id: string; from_node: string; status: string; port: string; output: unknown; error: string; wait_key: string; wake_at: string; started_at: string; finished_at: string }
export interface ActiveFlowRun { run: FlowRun; flow_name: string; open: string[]; open_names: string[]; card_title: string }
export interface FlowApproval { seq: number; run_id: string; flow_id: string; flow_name: string; node_id: string; card_id: string; card_title: string; question: string; since: string }
export interface FlowBlueprint { key: string; name: string; description: string; seeded: boolean; plugin_id?: string; plugin_name?: string }

// Plugins: what the hub says about them. A plugin never ships browser code; see lib/pluginView.ts.
export type PluginTarget = 'server' | 'runner'
export interface PluginPermission { key: string; label: string; risk: 'low' | 'medium' | 'high' | string; detail: string; target?: PluginTarget }
export interface PluginSettingDef { key: string; label: string; type: 'text' | 'textarea' | 'number' | 'bool' | 'select' | 'secret'; default?: unknown; options?: (string | { value: string; label?: string })[]; help?: string }
export interface PluginTargetsDecl { server?: { main?: string; permissions?: Record<string, unknown> }; runner?: { main?: string; permissions?: Record<string, unknown> } }
export interface PluginManifest {
  id: string
  name: string
  version: string
  description?: string
  author?: string
  homepage?: string
  icon?: string
  targets?: PluginTargetsDecl
  contributes?: { settings?: PluginSettingDef[] }
}
export interface InstalledPlugin { manifest: PluginManifest; status: string; enabled: boolean; granted?: string[]; error?: string; source?: string; targets?: PluginTargetsDecl }
export interface PluginInspection { token: string; manifest: PluginManifest; permissions: PluginPermission[]; upgradeFrom?: string; newPermissions?: unknown; warnings?: string[] }
export interface PluginCatalogEntry { id: string; name: string; description?: string; installed: boolean; version?: string }
export interface PluginRunnerInfo { runnerId: string; hostname: string; online: boolean; policy: string; delivered: boolean; version: string; error: string; paired?: boolean }
export interface PluginModelContribution { pluginId: string; id: string; value: string; label: string; mode: string; hint: string; icon: string; accent?: string; images: boolean; loader: string; waitingText: string; pluginName: string }
export interface PluginConversationContribution { pluginId: string; id: string; model: string; title: string; emptyText: string; showUrl: boolean }
// `ui`: the page has a frontend of its own (it runs in a Worker), and `render` is empty.
export interface PluginPageContribution { pluginId: string; id: string; label: string; icon: string; render: string; ui: boolean }
export interface PluginActionContribution { pluginId: string; id: string; label: string; icon: string; rpc: string }
export interface PluginUI { models: PluginModelContribution[]; conversations: PluginConversationContribution[]; pages: PluginPageContribution[]; chatActions: PluginActionContribution[]; cardActions: PluginActionContribution[] }
export interface PluginConversationRow {
  id: string
  session_id: string
  project_id: string
  plugin_id?: string
  source?: string
  url: string
  title: string
  turns?: number
  meta?: unknown
  created_at: string
  updated_at: string
  session_exists: boolean
  session_kind?: string
  message_count: number
  current: boolean
}
