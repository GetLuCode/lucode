import { useState, useEffect } from 'react'
import { IconPlus, IconTrash, IconEdit, IconChevronDown, IconChevronUp } from '@tabler/icons-react'
import { useStore } from '../../store'
import type { UserTool } from '../../types'

interface ToolFormData {
  name: string
  description: string
  input_schema: string
  code: string
  execution_env: 'browser' | 'orchestrator'
}

const EMPTY_FORM: ToolFormData = {
  name: '',
  description: '',
  input_schema: '{\n  "type": "object",\n  "properties": {},\n  "required": []\n}',
  code: '// args contains your tool arguments\n// return a value to send back as the result\nreturn { status: "ok" };',
  execution_env: 'browser',
}

function ToolForm({
  initial,
  onSave,
  onCancel,
}: {
  initial: ToolFormData
  onSave: (data: ToolFormData) => void
  onCancel: () => void
}) {
  const [form, setForm] = useState<ToolFormData>(initial)
  const [nameError, setNameError] = useState<string | null>(null)
  const [schemaError, setSchemaError] = useState<string | null>(null)

  const TOOL_NAME_RE = /^[a-zA-Z0-9_-]{1,128}$/

  const validateName = (n: string) => {
    if (!n.trim()) {
      setNameError('Name is required')
      return false
    }
    if (!TOOL_NAME_RE.test(n)) {
      setNameError('Only letters, numbers, underscores, and hyphens allowed (no spaces)')
      return false
    }
    setNameError(null)
    return true
  }

  const validateSchema = (s: string) => {
    try {
      const parsed = JSON.parse(s)
      if (typeof parsed !== 'object' || parsed === null) {
        setSchemaError('Schema must be a JSON object')
        return false
      }
      setSchemaError(null)
      return true
    } catch (e) {
      setSchemaError('Invalid JSON')
      return false
    }
  }

  const handleSubmit = () => {
    if (!validateName(form.name)) return
    if (!validateSchema(form.input_schema)) return
    onSave(form)
  }

  return (
    <div className="tool-form">
      <div className="tool-form__field">
        <label className="tool-form__label">
          Name
          {nameError && <span className="tool-form__error">{nameError}</span>}
        </label>
        <input
          className="tool-form__input"
          value={form.name}
          onChange={e => {
            setForm(f => ({ ...f, name: e.target.value }))
            if (nameError) setNameError(null)
          }}
          placeholder="e.g. ui_accept, quick_note"
        />
      </div>

      <div className="tool-form__field">
        <label className="tool-form__label">Description</label>
        <input
          className="tool-form__input"
          value={form.description}
          onChange={e => setForm(f => ({ ...f, description: e.target.value }))}
          placeholder="What does this tool do? (used by LLM to match commands)"
        />
      </div>

      <div className="tool-form__field">
        <label className="tool-form__label">
          Input Schema (JSON)
          {schemaError && <span className="tool-form__error">{schemaError}</span>}
        </label>
        <textarea
          className="tool-form__textarea tool-form__textarea--code"
          value={form.input_schema}
          onChange={e => {
            setForm(f => ({ ...f, input_schema: e.target.value }))
            validateSchema(e.target.value)
          }}
          rows={6}
          spellCheck={false}
        />
      </div>

      <div className="tool-form__field">
        <label className="tool-form__label">JavaScript Code</label>
        <textarea
          className="tool-form__textarea tool-form__textarea--code"
          value={form.code}
          onChange={e => setForm(f => ({ ...f, code: e.target.value }))}
          rows={8}
          spellCheck={false}
          placeholder="// Use 'args' to access input arguments&#10;// Return a value as the result"
        />
      </div>

      <div className="tool-form__field">
        <label className="tool-form__label">Execution Environment</label>
        <div className="tool-form__env-switch">
          <button
            className={`tool-form__env-btn${form.execution_env === 'browser' ? ' tool-form__env-btn--active' : ''}`}
            onClick={() => setForm(f => ({ ...f, execution_env: 'browser' }))}
          >
            Browser
          </button>
          <button
            className={`tool-form__env-btn${form.execution_env === 'orchestrator' ? ' tool-form__env-btn--active' : ''}`}
            onClick={() => setForm(f => ({ ...f, execution_env: 'orchestrator' }))}
          >
            Server (Goja)
          </button>
        </div>
        <span className="tool-form__hint">
          {form.execution_env === 'browser'
            ? 'Runs in the browser. Has access to DOM, fetch, localStorage.'
            : 'Runs on the server with Goja. Has access to fs, http, log modules.'}
        </span>
      </div>

      <div className="tool-form__actions">
        <button className="tool-form__btn tool-form__btn--cancel" onClick={onCancel}>
          Cancel
        </button>
        <button
          className="tool-form__btn tool-form__btn--save"
          onClick={handleSubmit}
          disabled={!form.name.trim()}
        >
          Save
        </button>
      </div>
    </div>
  )
}

export function ToolsTab() {
  const tools = useStore(s => s.userTools)
  const loadTools = useStore(s => s.loadUserTools)
  const createTool = useStore(s => s.createUserTool)
  const updateTool = useStore(s => s.updateUserTool)
  const deleteTool = useStore(s => s.deleteUserTool)

  const [showForm, setShowForm] = useState(false)
  const [editingTool, setEditingTool] = useState<UserTool | null>(null)
  const [expandedId, setExpandedId] = useState<string | null>(null)

  useEffect(() => { loadTools() }, [loadTools])

  const handleCreate = async (data: ToolFormData) => {
    await createTool(data)
    setShowForm(false)
  }

  const handleUpdate = async (data: ToolFormData) => {
    if (!editingTool) return
    await updateTool(editingTool.id, data)
    setEditingTool(null)
  }

  const handleDelete = async (id: string) => {
    await deleteTool(id)
    if (expandedId === id) setExpandedId(null)
  }

  return (
    <div className="tools-tab">
      <div className="tools-tab__header">
        <p className="tools-tab__desc">
          Define tools that can be triggered by voice commands. The LLM will match
          your spoken transcript to the appropriate tool and extract arguments.
        </p>
        {!showForm && !editingTool && (
          <button className="tool-form__btn tool-form__btn--save" onClick={() => setShowForm(true)}>
            <IconPlus size={14} />
            <span>New Tool</span>
          </button>
        )}
      </div>

      {showForm && (
        <ToolForm
          initial={EMPTY_FORM}
          onSave={handleCreate}
          onCancel={() => setShowForm(false)}
        />
      )}

      {editingTool && (
        <ToolForm
          initial={{
            name: editingTool.name,
            description: editingTool.description,
            input_schema: editingTool.input_schema,
            code: editingTool.code,
            execution_env: editingTool.execution_env,
          }}
          onSave={handleUpdate}
          onCancel={() => setEditingTool(null)}
        />
      )}

      <div className="tools-tab__list">
        {tools.length === 0 && !showForm && (
          <div className="tools-tab__empty">
            No tools defined yet. Create one to get started.
          </div>
        )}

        {tools.map(tool => (
          <div key={tool.id} className="tools-tab__item">
            <div
              className="tools-tab__item-header"
              onClick={() => setExpandedId(expandedId === tool.id ? null : tool.id)}
            >
              <div className="tools-tab__item-info">
                <span className="tools-tab__item-name">{tool.name}</span>
                <span className={`tools-tab__item-env tools-tab__item-env--${tool.execution_env}`}>
                  {tool.execution_env === 'browser' ? 'Browser' : 'Server'}
                </span>
              </div>
              <div className="tools-tab__item-actions">
                <button
                  className="icon-btn"
                  onClick={e => { e.stopPropagation(); setEditingTool(tool); setShowForm(false) }}
                  title="Edit tool"
                >
                  <IconEdit size={14} />
                </button>
                <button
                  className="icon-btn tools-tab__delete-btn"
                  onClick={e => { e.stopPropagation(); handleDelete(tool.id) }}
                  title="Delete tool"
                >
                  <IconTrash size={14} />
                </button>
                {expandedId === tool.id
                  ? <IconChevronUp size={14} />
                  : <IconChevronDown size={14} />}
              </div>
            </div>

            {expandedId === tool.id && (
              <div className="tools-tab__item-detail">
                {tool.description && (
                  <p className="tools-tab__item-desc">{tool.description}</p>
                )}
                <div className="tools-tab__item-section">
                  <span className="tools-tab__item-section-label">Schema</span>
                  <pre className="tools-tab__item-code">{tool.input_schema}</pre>
                </div>
                <div className="tools-tab__item-section">
                  <span className="tools-tab__item-section-label">Code</span>
                  <pre className="tools-tab__item-code">{tool.code}</pre>
                </div>
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}
