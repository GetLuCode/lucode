import { IconX } from '@tabler/icons-react'
import { useEffect, useState } from 'react'
import * as api from '../../api'
import type { AIProfile, Project, Runner } from '../../types'

// Changes where a project's agents run and with which account: its runner, the
// work dir on that runner (peers only) and one of that runner's preconfigured AIs.
export function ProjectEnvironmentModal({
  project,
  onClose,
  onSaved,
}: {
  project: Project
  onClose: () => void
  onSaved: () => void
}) {
  const [runners, setRunners] = useState<Runner[]>([])
  const [runnerId, setRunnerId] = useState(project.runner_id ?? '') // '' = this instance
  const [aiId, setAiId] = useState(project.ai_profile_id ?? '')
  const [dir, setDir] = useState((project.runner_id && project.env_dirs?.[project.runner_id]?.[0]) || '')
  const [profiles, setProfiles] = useState<AIProfile[]>([])
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => { api.listRunners().then(setRunners).catch(() => setRunners([])) }, [])
  useEffect(() => {
    api.listAIProfiles(runnerId || 'local').then(setProfiles).catch(() => setProfiles([]))
  }, [runnerId])

  const changeRunner = (id: string) => {
    setRunnerId(id)
    setAiId('') // an AI belongs to its runner
    setDir(id ? project.env_dirs?.[id]?.[0] ?? '' : '')
  }

  const save = async () => {
    setSaving(true)
    setError('')
    try {
      await api.updateProjectEnvironment(project.id, {
        runner_id: runnerId,
        ai_profile_id: aiId,
        ...(runnerId && dir.trim() ? { env_dirs: { ...(project.env_dirs ?? {}), [runnerId]: [dir.trim()] } } : {}),
      })
      onSaved()
      onClose()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="workspace-picker-overlay" onClick={onClose}>
      <div className="workspace-picker-modal" onClick={e => e.stopPropagation()}>
        <div className="workspace-picker__header">
          <h2>{project.name} · Environment</h2>
          <button className="workspace-picker__close" onClick={onClose} aria-label="Close">
            <IconX size={16} />
          </button>
        </div>

        <div className="workspace-picker__name-input">
          <label>Runner</label>
          <select className="input" value={runnerId} onChange={e => changeRunner(e.target.value)}>
            {runners.map(r => (
              <option key={r.id} value={r.self ? '' : r.id}>
                {r.hostname}{r.self ? ' (this)' : ''}
              </option>
            ))}
          </select>
        </div>

        {runnerId && (
          <div className="workspace-picker__name-input">
            <label>Work dir on that runner</label>
            <input
              className="input"
              value={dir}
              onChange={e => setDir(e.target.value)}
              placeholder="Absolute path on the runner"
            />
          </div>
        )}

        <div className="workspace-picker__name-input">
          <label>AI</label>
          <select className="input" value={aiId} onChange={e => setAiId(e.target.value)}>
            <option value="">Default account</option>
            {profiles.map(p => (
              <option key={p.id} value={p.id}>{p.name}{p.email ? ` — ${p.email}` : ''}</option>
            ))}
          </select>
        </div>

        {error && <div className="workspace-picker__message error">{error}</div>}
        <div className="workspace-picker__footer">
          <button className="btn btn--secondary" onClick={onClose}>Cancel</button>
          <button className="btn btn--primary" onClick={save} disabled={saving || (!!runnerId && !dir.trim())}>
            Save
          </button>
        </div>
      </div>
    </div>
  )
}
