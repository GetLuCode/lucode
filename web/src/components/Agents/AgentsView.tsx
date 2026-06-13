import { useEffect, useState } from 'react'
import { IconPlus, IconTrash, IconEdit, IconRobot, IconX } from '@tabler/icons-react'
import { useStore } from '../../store'
import type { Agent } from '../../types'
import * as api from '../../api'

const MODELS = [
  { label: 'Haiku 4.5',  value: 'haiku' },
  { label: 'Sonnet 4.6', value: 'sonnet' },
  { label: 'Opus 4.6',   value: 'opus' },
]

const ALL_TOOLS = [
  { name: 'Bash',    desc: 'Execute shell commands' },
  { name: 'Edit',    desc: 'Edit file contents' },
  { name: 'Read',    desc: 'Read files from disk' },
  { name: 'Write',   desc: 'Write files to disk' },
  { name: 'Glob',    desc: 'Find files by pattern' },
  { name: 'Grep',    desc: 'Search file contents' },
  { name: 'WebFetch', desc: 'Fetch URLs from the web' },
  { name: 'WebSearch', desc: 'Search the web' },
]

const DEFAULT_TOOLS = ['Bash', 'Edit', 'Read', 'Write']

interface AgentFormData {
  name: string
  model: string
  account_id: string  // legacy field — no longer drives anything, kept for ABI
  personality: string
  tools: string[]
}

const EMPTY_FORM: AgentFormData = {
  name: '',
  model: 'sonnet',
  account_id: '',
  personality: '',
  tools: [...DEFAULT_TOOLS],
}

function AgentForm({
  initial,
  onSave,
  onCancel,
}: {
  initial?: AgentFormData
  onSave: (data: AgentFormData) => Promise<void>
  onCancel: () => void
}) {
  const [form, setForm] = useState<AgentFormData>(initial ?? EMPTY_FORM)
  const [activeTab, setActiveTab] = useState<'general' | 'tools'>('general')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

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
        >Tools <span className="agent-form__tab-badge">{form.tools.length}</span></button>
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

          <div className="agent-form__row">
            <div className="agent-form__field">
              <label className="agent-form__label">Model</label>
              <select
                className="agent-form__select"
                value={form.model}
                onChange={e => setField('model', e.target.value)}
              >
                {MODELS.map(m => (
                  <option key={m.value} value={m.value}>{m.label}</option>
                ))}
              </select>
            </div>
          </div>

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
          <p className="agent-form__tools-hint">Enable the tools this agent can use.</p>
          <div className="agent-form__tools-list">
            {ALL_TOOLS.map(t => {
              const enabled = form.tools.includes(t.name)
              return (
                <label key={t.name} className="agent-form__tool-row">
                  <div className="agent-form__tool-info">
                    <span className="agent-form__tool-name">{t.name}</span>
                    <span className="agent-form__tool-desc">{t.desc}</span>
                  </div>
                  <button
                    type="button"
                    role="switch"
                    aria-checked={enabled}
                    className={`agent-form__switch${enabled ? ' agent-form__switch--on' : ''}`}
                    onClick={() => toggleTool(t.name)}
                  >
                    <span className="agent-form__switch-thumb" />
                  </button>
                </label>
              )
            })}
          </div>
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
  onEdit,
  onDelete,
}: {
  agent: Agent
  onEdit: () => void
  onDelete: () => void
}) {
  const modelLabel = MODELS.find(m => m.value === agent.model)?.label ?? agent.model

  return (
    <div className="agent-card">
      <div className="agent-card__icon">
        <IconRobot size={20} />
      </div>
      <div className="agent-card__body">
        <div className="agent-card__header">
          <span className="agent-card__name">{agent.name}</span>
          <span className="agent-card__model">{modelLabel}</span>
        </div>
        {agent.personality && (
          <p className="agent-card__personality">{agent.personality}</p>
        )}
        <div className="agent-card__tools">
          {agent.tools.map(t => (
            <span key={t} className="agent-card__tool-badge">{t}</span>
          ))}
        </div>
      </div>
      <div className="agent-card__actions">
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
    const agent = await api.updateAgent(id, data)
    setAgents(prev => prev.map(a => a.id === id ? agent : a))
    setView('list')
  }

  async function handleDelete(id: string) {
    if (!confirm('Delete this agent?')) return
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
          }}
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
              onEdit={() => setView({ edit: agent })}
              onDelete={() => handleDelete(agent.id)}
            />
          ))}
        </div>
      )}
    </div>
  )
}
