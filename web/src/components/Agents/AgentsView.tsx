import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { IconPlus, IconTrash, IconEdit, IconRobot, IconX, IconMessages } from '@tabler/icons-react'
import { useStore } from '../../store'
import type { Agent, AIProfile, Runner, Session } from '../../types'
import * as api from '../../api'

const MODELS = [
  { label: 'Haiku 5.5',  value: 'haiku' },
  { label: 'Sonnet 5.5', value: 'sonnet' },
  { label: 'Opus 5.5',   value: 'opus' },
  { label: 'MiniMax M3', value: 'MiniMax-M3' },
  { label: 'MiniMax M2.7', value: 'MiniMax-M2.7' },
]

const EXECUTORS = [
  { label: 'aiapi (API service)', value: 'aiapi' },
  { label: 'claude -p (local CLI)', value: 'claude' },
] as const

// The built-in tools `claude -p` reports in its init event. ToolSearch is left out:
// it is how claude loads the lucode task tools, so it is always available.
const ALL_TOOLS = [
  { name: 'Bash',                 desc: 'Execute shell commands' },
  { name: 'PowerShell',           desc: 'Execute PowerShell commands' },
  { name: 'Read',                 desc: 'Read files from disk' },
  { name: 'Edit',                 desc: 'Edit file contents' },
  { name: 'Write',                desc: 'Write files to disk' },
  { name: 'NotebookEdit',         desc: 'Edit Jupyter notebooks' },
  { name: 'Glob',                 desc: 'Find files by pattern' },
  { name: 'Grep',                 desc: 'Search file contents' },
  { name: 'WebFetch',             desc: 'Fetch URLs from the web' },
  { name: 'WebSearch',            desc: 'Search the web' },
  { name: 'Task',                 desc: 'Run a sub-agent for a subtask' },
  { name: 'TaskStop',             desc: 'Stop a running background task' },
  { name: 'Skill',                desc: 'Run a skill' },
  { name: 'Monitor',              desc: 'Watch a background process' },
  { name: 'EnterWorktree',        desc: 'Work in an isolated git worktree' },
  { name: 'ExitWorktree',         desc: 'Leave the git worktree' },
  { name: 'ListAgents',           desc: 'List other agents' },
  { name: 'SendMessage',          desc: 'Send a message to another agent' },
  { name: 'SendToProject',        desc: 'Send a message to another project' },
  { name: 'SendToProjectContext', desc: 'Send context to another project' },
  { name: 'RemoteTrigger',        desc: 'Trigger a remote agent' },
  { name: 'CronCreate',           desc: 'Schedule a recurring prompt' },
  { name: 'CronList',             desc: 'List scheduled prompts' },
  { name: 'CronDelete',           desc: 'Remove a scheduled prompt' },
  { name: 'ScheduleWakeup',       desc: 'Schedule the next wake-up of a loop' },
  { name: 'PushNotification',     desc: 'Send a notification to the user' },
  { name: 'ReportFindings',       desc: 'Report code review findings' },
  { name: 'DesignSync',           desc: 'Sync designs' },
]

// The tools lucode itself gives an agent, as opposed to the built-in Claude Code ones above.
const LUCODE_TOOLS = [
  { name: 'task_comment',  desc: 'Post a progress note on the task' },
  { name: 'task_done',     desc: 'Mark the task as done (moves it to Done)' },
  { name: 'task_blocked',  desc: 'Mark the task as blocked (moves it to Blocked)' },
  { name: 'agent_list',    desc: 'See the other agents and what they are working on' },
  { name: 'send_to_agent', desc: 'Send a message to another agent' },
  { name: 'task_info',     desc: 'Look up a task of the project' },
]

const DEFAULT_TOOLS = ['Bash', 'Edit', 'Read', 'Write']

// One category of tools with its own on/off switches.
function ToolGroup({ title, hint, tools, enabled, onToggle }: {
  title: string
  hint: string
  tools: { name: string; desc: string }[]
  enabled: string[]
  onToggle: (name: string) => void
}) {
  return (
    <div className="agent-form__tool-group">
      <div className="agent-form__tool-group-title">
        {title}
        <span className="agent-form__tab-badge">{tools.filter(t => enabled.includes(t.name)).length}/{tools.length}</span>
      </div>
      <p className="agent-form__tools-hint">{hint}</p>
      <div className="agent-form__tools-list">
        {tools.map(t => {
          const on = enabled.includes(t.name)
          return (
            <label key={t.name} className="agent-form__tool-row">
              <div className="agent-form__tool-info">
                <span className="agent-form__tool-name">{t.name}</span>
                <span className="agent-form__tool-desc">{t.desc}</span>
              </div>
              <button
                type="button"
                role="switch"
                aria-checked={on}
                className={`agent-form__switch${on ? ' agent-form__switch--on' : ''}`}
                onClick={() => onToggle(t.name)}
              >
                <span className="agent-form__switch-thumb" />
              </button>
            </label>
          )
        })}
      </div>
    </div>
  )
}

interface AgentFormData {
  name: string
  model: string
  account_id: string  // legacy field — no longer drives anything, kept for ABI
  personality: string
  tools: string[]
  executor: 'aiapi' | 'claude'
  persistent: boolean
  reports_to: string
  scope: 'project' | 'global'
  lucode_tools: string[]
  runner_id: string     // '' = the project's; 'local' = this instance
  ai_profile_id: string // '' = the project's
}

const EMPTY_FORM: AgentFormData = {
  name: '',
  model: 'sonnet',
  account_id: '',
  personality: '',
  tools: [...DEFAULT_TOOLS],
  executor: 'aiapi',
  persistent: false,
  reports_to: '',
  scope: 'project',
  lucode_tools: LUCODE_TOOLS.map(t => t.name),
  runner_id: '',
  ai_profile_id: '',
}

function AgentForm({
  initial,
  selfId,
  teammates,
  onSave,
  onCancel,
}: {
  initial?: AgentFormData
  selfId?: string
  teammates: Agent[]
  onSave: (data: AgentFormData) => Promise<void>
  onCancel: () => void
}) {
  const [form, setForm] = useState<AgentFormData>(initial ?? EMPTY_FORM)
  const supervisors = teammates.filter(a => a.id !== selfId && (form.scope !== 'global' || a.scope === 'global'))
  const [activeTab, setActiveTab] = useState<'general' | 'tools'>('general')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  // Environment: the agent can pick its own runner and AI; empty means the project's.
  const project = useStore(s => s.projects.find(p => p.id === s.activeProjectId))
  const [runners, setRunners] = useState<Runner[]>([])
  const [aiProfiles, setAiProfiles] = useState<AIProfile[]>([])
  useEffect(() => { api.listRunners().then(setRunners).catch(() => setRunners([])) }, [])
  // The AIs offered are those of the runner the agent will use.
  const aiRunner = form.runner_id || project?.runner_id || 'local'
  useEffect(() => {
    api.listAIProfiles(aiRunner).then(setAiProfiles).catch(() => setAiProfiles([]))
  }, [aiRunner])

  function setField<K extends keyof AgentFormData>(k: K, v: AgentFormData[K]) {
    setForm(f => ({ ...f, [k]: v }))
  }

  function toggleTool(tool: string) {
    setForm(f => ({
      ...f,
      tools: f.tools.includes(tool)
        ? f.tools.filter(t => t !== tool)
        : [...f.tools, tool],
    }))
  }

  function toggleLucodeTool(tool: string) {
    setForm(f => ({
      ...f,
      lucode_tools: f.lucode_tools.includes(tool)
        ? f.lucode_tools.filter(t => t !== tool)
        : [...f.lucode_tools, tool],
    }))
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!form.name.trim()) { setError('Name is required'); return }
    setSaving(true)
    setError('')
    try {
      await onSave({ ...form, name: form.name.trim() })
    } catch (err) {
      setError((err as Error).message)
      setSaving(false)
    }
  }

  return (
    <form className="agent-form" onSubmit={handleSubmit}>
      <div className="agent-form__tabs">
        <button
          type="button"
          className={`agent-form__tab${activeTab === 'general' ? ' agent-form__tab--active' : ''}`}
          onClick={() => setActiveTab('general')}
        >General</button>
        <button
          type="button"
          className={`agent-form__tab${activeTab === 'tools' ? ' agent-form__tab--active' : ''}`}
          onClick={() => setActiveTab('tools')}
        >Tools <span className="agent-form__tab-badge">{form.tools.length + form.lucode_tools.length}</span></button>
      </div>

      {activeTab === 'general' && (
        <div className="agent-form__body">
          <div className="agent-form__field">
            <label className="agent-form__label">Name</label>
            <input
              className="agent-form__input"
              value={form.name}
              onChange={e => setField('name', e.target.value)}
              placeholder="e.g. Code Reviewer"
              autoFocus
            />
          </div>

          <div className="agent-form__field">
            <label className="agent-form__label">Available in</label>
            <select
              className="agent-form__select"
              value={form.scope}
              onChange={e => {
                const scope = e.target.value as AgentFormData['scope']
                setField('scope', scope)
                // a global agent can only report to another global agent
                if (scope === 'global' && teammates.find(a => a.id === form.reports_to)?.scope === 'project') setField('reports_to', '')
              }}
            >
              <option value="project">This project only</option>
              <option value="global">All projects (shared)</option>
            </select>
            {form.scope === 'global' && (
              <p style={{ fontSize: 11, color: 'var(--text-muted)', margin: '4px 0 0' }}>
                Shared by every project: editing or deleting it affects all of them.
              </p>
            )}
          </div>

          <div className="agent-form__row">
            <div className="agent-form__field">
              <label className="agent-form__label">Model</label>
              <select
                className="agent-form__select"
                value={form.model}
                onChange={e => setField('model', e.target.value)}
              >
                {MODELS.filter(m => form.executor !== 'claude' || !m.value.startsWith('MiniMax')).map(m => (
                  <option key={m.value} value={m.value}>{m.label}</option>
                ))}
              </select>
            </div>
            <div className="agent-form__field">
              <label className="agent-form__label">Runs with</label>
              <select
                className="agent-form__select"
                value={form.executor}
                onChange={e => {
                  const executor = e.target.value as AgentFormData['executor']
                  setField('executor', executor)
                  // the claude CLI only runs Claude models
                  if (executor === 'claude' && form.model.startsWith('MiniMax')) setField('model', 'sonnet')
                  // only the claude CLI can keep a process alive
                  if (executor !== 'claude') setField('persistent', false)
                }}
              >
                {EXECUTORS.map(x => (
                  <option key={x.value} value={x.value}>{x.label}</option>
                ))}
              </select>
            </div>
          </div>

          <div className="agent-form__row">
            <div className="agent-form__field">
              <label className="agent-form__label">Reports to</label>
              <select
                className="agent-form__select"
                value={form.reports_to}
                onChange={e => setField('reports_to', e.target.value)}
              >
                <option value="">Nobody</option>
                {supervisors.map(a => (
                  <option key={a.id} value={a.id}>{a.name}</option>
                ))}
              </select>
            </div>
            <div className="agent-form__field">
              <label className="agent-form__label">Process</label>
              <label style={{ display: 'flex', alignItems: 'center', gap: 8, fontSize: 13, opacity: form.executor === 'claude' ? 1 : 0.5 }}>
                <input
                  type="checkbox"
                  checked={form.persistent && form.executor === 'claude'}
                  disabled={form.executor !== 'claude'}
                  onChange={e => setField('persistent', e.target.checked)}
                />
                Keep it alive between messages
              </label>
            </div>
          </div>
          <p style={{ fontSize: 11, color: 'var(--text-muted)', margin: '-4px 0 8px' }}>
            Reports to: this agent&apos;s supervisor is told when it finishes, gets blocked or fails, and can be
            reached with agent_message. Keep alive (claude -p only): replies faster, but each active task holds a
            claude process in memory; it exits by itself after 30 idle minutes and the conversation resumes later.
          </p>

          <div className="agent-form__row">
            <div className="agent-form__field">
              <label className="agent-form__label">Runner</label>
              <select
                className="agent-form__select"
                value={form.runner_id}
                onChange={e => { setField('runner_id', e.target.value); setField('ai_profile_id', '') }}
              >
                <option value="">Project&apos;s runner</option>
                {runners.map(r => (
                  <option key={r.id} value={r.self ? 'local' : r.id}>
                    {r.hostname}{r.self ? ' (this)' : ''}
                  </option>
                ))}
              </select>
            </div>
            <div className="agent-form__field">
              <label className="agent-form__label">AI</label>
              <select
                className="agent-form__select"
                value={form.ai_profile_id}
                onChange={e => setField('ai_profile_id', e.target.value)}
              >
                <option value="">{form.runner_id ? 'Default account' : "Project's AI"}</option>
                {aiProfiles.map(p => (
                  <option key={p.id} value={p.id}>{p.name}{p.email ? ` — ${p.email}` : ''}</option>
                ))}
              </select>
            </div>
          </div>
          <p style={{ fontSize: 11, color: 'var(--text-muted)', margin: '-4px 0 8px' }}>
            Where the agent runs and with which account. Empty uses the project&apos;s. Only claude -p agents run on
            other runners, and the project needs a work dir on that runner.
          </p>

          <div className="agent-form__field">
            <label className="agent-form__label">Personality</label>
            <textarea
              className="agent-form__textarea"
              value={form.personality}
              onChange={e => setField('personality', e.target.value)}
              placeholder="Describe the agent's personality, role, and behaviour…"
              rows={5}
            />
          </div>
        </div>
      )}

      {activeTab === 'tools' && (
        <div className="agent-form__body">
          <ToolGroup
            title="Claude tools"
            hint="Built-in Claude Code tools. Only the ones switched on are offered to the agent (none selected = all of them). With aiapi they map onto its own set: Read, Glob, Grep, Edit/Write, Bash/PowerShell."
            tools={ALL_TOOLS}
            enabled={form.tools}
            onToggle={toggleTool}
          />
          <ToolGroup
            title="Lucode tools"
            hint="Provided by lucode so the agent can report on its task and work with other agents. Switch off what this agent should not do, for example send_to_agent to keep it from messaging others."
            tools={LUCODE_TOOLS}
            enabled={form.lucode_tools}
            onToggle={toggleLucodeTool}
          />
        </div>
      )}

      {error && <div className="agent-form__error">{error}</div>}

      <div className="agent-form__actions">
        <button type="button" className="agent-form__btn agent-form__btn--cancel" onClick={onCancel}>
          Cancel
        </button>
        <button type="submit" className="agent-form__btn agent-form__btn--save" disabled={saving}>
          {saving ? 'Saving…' : (initial ? 'Save changes' : 'Create agent')}
        </button>
      </div>
    </form>
  )
}

function AgentCard({
  agent,
  supervisor,
  onEdit,
  onDelete,
}: {
  agent: Agent
  supervisor?: string
  onEdit: () => void
  onDelete: () => void
}) {
  const modelLabel = MODELS.find(m => m.value === agent.model)?.label ?? agent.model
  const navigate = useNavigate()
  const setActiveSession = useStore(s => s.setActiveSession)
  const [sessions, setSessions] = useState<Session[] | null>(null)

  // Inspect: list the task sessions this agent has run and jump into one.
  async function toggleSessions() {
    if (sessions) { setSessions(null); return }
    setSessions(await api.listAgentSessions(agent.id).catch(() => []))
  }

  return (
    <div className="agent-card">
      <div className="agent-card__icon">
        <IconRobot size={20} />
      </div>
      <div className="agent-card__body">
        <div className="agent-card__header">
          <span className="agent-card__name">{agent.name}</span>
          <span className="agent-card__model">{modelLabel}</span>
          <span className="agent-card__model">{agent.executor === 'claude' ? 'claude -p' : 'aiapi'}</span>
          {agent.scope === 'global' && <span className="agent-card__model" title="Available in every project">global</span>}
          {agent.persistent && <span className="agent-card__model" title="Process stays alive between messages">live</span>}
          {supervisor && <span className="agent-card__model" title="Reports to">→ {supervisor}</span>}
        </div>
        {agent.personality && (
          <p className="agent-card__personality">{agent.personality}</p>
        )}
        <div className="agent-card__tools">
          {agent.tools.map(t => (
            <span key={t} className="agent-card__tool-badge">{t}</span>
          ))}
        </div>
        {sessions && (
          <div className="agent-card__sessions">
            {sessions.length === 0 && <span className="agent-card__personality">No tasks run yet.</span>}
            {sessions.map(s => (
              <button
                key={s.id}
                className="agent-card__tool-badge"
                title={`${s.message_count} messages`}
                onClick={async () => {
                  await setActiveSession(s.id)
                  navigate(`/projects/${s.project_id}/sessions/${s.id}`)
                }}
              >
                {s.name || 'Untitled task'}
              </button>
            ))}
          </div>
        )}
      </div>
      <div className="agent-card__actions">
        <button className="agent-card__btn" title="Inspect tasks / sessions" onClick={toggleSessions}>
          <IconMessages size={14} />
        </button>
        <button className="agent-card__btn" title="Edit" onClick={onEdit}>
          <IconEdit size={14} />
        </button>
        <button className="agent-card__btn agent-card__btn--danger" title="Delete" onClick={onDelete}>
          <IconTrash size={14} />
        </button>
      </div>
    </div>
  )
}

type ViewState = 'list' | 'create' | { edit: Agent }

export function AgentsView() {
  const activeProjectId = useStore(s => s.activeProjectId)

  const [agents, setAgents] = useState<Agent[]>([])
  const [view, setView] = useState<ViewState>('list')
  const [loading, setLoading] = useState(false)
  const [_deleted, setDeleted] = useState<string | null>(null)

  useEffect(() => {
    if (!activeProjectId) return
    setLoading(true)
    api.listAgents(activeProjectId)
      .then(setAgents)
      .catch(() => setAgents([]))
      .finally(() => setLoading(false))
  }, [activeProjectId])

  async function handleCreate(data: AgentFormData) {
    if (!activeProjectId) return
    const agent = await api.createAgent(activeProjectId, data)
    setAgents(prev => [...prev, agent])
    setView('list')
  }

  async function handleUpdate(id: string, data: AgentFormData) {
    // project_id only matters when a global agent is made project-only: it says which project
    const agent = await api.updateAgent(id, { ...data, project_id: activeProjectId ?? undefined })
    setAgents(prev => prev.map(a => a.id === id ? agent : a))
    setView('list')
  }

  async function handleDelete(id: string) {
    const target = agents.find(a => a.id === id)
    if (!confirm(target?.scope === 'global'
      ? 'Delete this global agent? It will disappear from every project.'
      : 'Delete this agent?')) return
    setDeleted(id)
    try {
      await api.deleteAgent(id)
      setAgents(prev => prev.filter(a => a.id !== id))
    } finally {
      setDeleted(null)
    }
  }

  if (!activeProjectId) {
    return <div className="agents-view agents-view--empty">Select a project to view agents.</div>
  }

  return (
    <div className="agents-view">
      <div className="agents-view__header">
        <h2 className="agents-view__title">Agents</h2>
        {view === 'list' && (
          <button
            className="agents-view__new-btn"
            onClick={() => setView('create')}
            title="New agent"
          >
            <IconPlus size={14} />
            New agent
          </button>
        )}
        {view !== 'list' && (
          <button
            className="agents-view__close-btn"
            onClick={() => setView('list')}
            title="Back to list"
          >
            <IconX size={16} />
          </button>
        )}
      </div>

      {view === 'create' && (
        <AgentForm
          teammates={agents}
          onSave={handleCreate}
          onCancel={() => setView('list')}
        />
      )}

      {typeof view === 'object' && 'edit' in view && (
        <AgentForm
          initial={{
            name: view.edit.name,
            model: view.edit.model,
            account_id: view.edit.account_id,
            personality: view.edit.personality,
            tools: view.edit.tools,
            executor: view.edit.executor ?? 'aiapi',
            persistent: view.edit.persistent ?? false,
            reports_to: view.edit.reports_to ?? '',
            scope: view.edit.scope ?? 'project',
            lucode_tools: view.edit.lucode_tools ?? LUCODE_TOOLS.map(t => t.name),
            runner_id: view.edit.runner_id ?? '',
            ai_profile_id: view.edit.ai_profile_id ?? '',
          }}
          selfId={view.edit.id}
          teammates={agents}
          onSave={data => handleUpdate(view.edit.id, data)}
          onCancel={() => setView('list')}
        />
      )}

      {view === 'list' && (
        <div className="agents-view__list">
          {loading && (
            <div className="agents-view__loading">Loading…</div>
          )}
          {!loading && agents.length === 0 && (
            <div className="agents-view__empty">
              <IconRobot size={32} />
              <p>No agents yet. Create one to get started.</p>
            </div>
          )}
          {agents.map(agent => (
            <AgentCard
              key={agent.id}
              agent={agent}
              supervisor={agents.find(x => x.id === agent.reports_to)?.name}
              onEdit={() => setView({ edit: agent })}
              onDelete={() => handleDelete(agent.id)}
            />
          ))}
        </div>
      )}
    </div>
  )
}
