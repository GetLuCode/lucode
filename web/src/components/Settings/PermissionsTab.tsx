import { useEffect, useState } from 'react'
import { IconTrash, IconPlus, IconExternalLink } from '@tabler/icons-react'
import { useStore } from '../../store'
import {
  listSessionPermissions,
  createSessionPermission,
  deleteSessionPermission,
  type SessionPermissionsResponse,
} from '../../api'
import type { PermissionRule, PermissionScope } from '../../types'

// Permissions tab — shows the merged permission view for the active
// session (user file + project file + session overrides). User/project
// rules are read-only here; the user edits the JSON file at the path
// shown. Session-scope rules can be added and removed.
export function PermissionsTab() {
  const activeSessionId = useStore(s => s.activeSessionId)
  const [data, setData] = useState<SessionPermissionsResponse | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [showForm, setShowForm] = useState(false)

  const refresh = async () => {
    if (!activeSessionId) return
    setLoading(true)
    setError(null)
    try {
      const resp = await listSessionPermissions(activeSessionId)
      setData(resp)
    } catch (e) {
      setError(String(e))
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => { refresh() }, [activeSessionId])

  if (!activeSessionId) {
    return (
      <div className="permissions-tab__empty">
        Select a session to manage its permission rules.
      </div>
    )
  }

  return (
    <div className="permissions-tab">
      <div className="permissions-tab__header">
        <p className="permissions-tab__desc">
          Tool execution rules are evaluated in order: <code>deny</code> &rarr; <code>allow</code> &rarr; <code>ask</code> &rarr; prompt.
          The most specific rule (more regex fields) wins. User-level rules apply across all projects;
          project-level rules are committed to git and shared with teammates; session rules are local.
        </p>
        {!showForm && (
          <button className="tool-form__btn tool-form__btn--save" onClick={() => setShowForm(true)}>
            <IconPlus size={14} />
            <span>Add session rule</span>
          </button>
        )}
      </div>

      {data && (
        <div className="permissions-tab__files">
          <FileRow label="User settings" path={data.user_file} />
          <FileRow label="Project settings" path={data.project_file} />
        </div>
      )}

      {showForm && data && (
        <NewRuleForm
          onSave={async (rule) => {
            await createSessionPermission(activeSessionId, rule)
            setShowForm(false)
            await refresh()
          }}
          onCancel={() => setShowForm(false)}
        />
      )}

      {error && <div className="permissions-tab__error">{error}</div>}

      {loading && !data && <div className="permissions-tab__empty">Loading…</div>}

      {data && (
        <div className="permissions-tab__list">
          <h3 className="permissions-tab__section-title">Session rules</h3>
          {data.session_rules.length === 0 ? (
            <div className="permissions-tab__empty">No session-scope rules. Use "Allow always" or "Edit permission" in a pending tool-use bubble to create one.</div>
          ) : (
            data.session_rules.map((r) => (
              <SessionRuleRow
                key={r.id}
                rule={r}
                onDelete={async () => {
                  await deleteSessionPermission(activeSessionId, r.id)
                  await refresh()
                }}
              />
            ))
          )}
        </div>
      )}
    </div>
  )
}

function FileRow({ label, path }: { label: string; path: string }) {
  return (
    <div className="permissions-tab__file">
      <span className="permissions-tab__file-label">{label}</span>
      <code className="permissions-tab__file-path">{path || '(unset)'}</code>
      {path && (
        <a
          className="permissions-tab__file-link"
          href={`file:///${path.replace(/\\/g, '/')}`}
          target="_blank"
          rel="noreferrer"
          title="Open in editor"
        >
          <IconExternalLink size={12} />
        </a>
      )}
    </div>
  )
}

function SessionRuleRow({ rule, onDelete }: { rule: PermissionRule; onDelete: () => void }) {
  return (
    <div className="permissions-tab__rule">
      <div className="permissions-tab__rule-header">
        <span className={`permissions-tab__scope permissions-tab__scope--${rule.scope}`}>{rule.scope}</span>
        <span className="permissions-tab__tool">{rule.tool}</span>
        <button className="icon-btn permissions-tab__delete-btn" onClick={onDelete} title="Delete">
          <IconTrash size={14} />
        </button>
      </div>
      {rule.input_regex && Object.keys(rule.input_regex).length > 0 && (
        <div className="permissions-tab__regex">
          {Object.entries(rule.input_regex).map(([k, v]) => (
            <div key={k} className="permissions-tab__regex-row">
              <span className="permissions-tab__regex-field">{k}</span>
              <code className="permissions-tab__regex-pattern">{v}</code>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function NewRuleForm({
  onSave,
  onCancel,
}: {
  onSave: (rule: { tool: string; input_regex?: Record<string, string>; scope: PermissionScope }) => Promise<void>
  onCancel: () => void
}) {
  const [tool, setTool] = useState('')
  const [scope, setScope] = useState<PermissionScope>('allow')
  const [fieldName, setFieldName] = useState('path')
  const [regex, setRegex] = useState('')

  const handleSave = async () => {
    if (!tool.trim()) return
    const input_regex = regex.trim() ? { [fieldName]: regex.trim() } : undefined
    await onSave({ tool: tool.trim(), input_regex, scope })
  }

  return (
    <div className="permissions-tab__form">
      <label className="permissions-tab__form-row">
        <span>Tool</span>
        <input value={tool} onChange={(e) => setTool(e.target.value)} placeholder="e.g. read_file, Bash" />
      </label>
      <label className="permissions-tab__form-row">
        <span>Scope</span>
        <select value={scope} onChange={(e) => setScope(e.target.value as PermissionScope)}>
          <option value="allow">allow</option>
          <option value="deny">deny</option>
          <option value="ask">ask</option>
        </select>
      </label>
      <label className="permissions-tab__form-row">
        <span>Field</span>
        <input value={fieldName} onChange={(e) => setFieldName(e.target.value)} placeholder="e.g. path, command, url" />
      </label>
      <label className="permissions-tab__form-row">
        <span>Regex</span>
        <input value={regex} onChange={(e) => setRegex(e.target.value)} placeholder="^\\./src/.* (leave empty to match any input)" />
      </label>
      <div className="permissions-tab__form-actions">
        <button className="btn" onClick={onCancel}>Cancel</button>
        <button className="btn btn--approve" onClick={handleSave}>Save</button>
      </div>
    </div>
  )
}
