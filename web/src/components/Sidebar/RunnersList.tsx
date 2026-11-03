import { IconBrandApple, IconBrandUbuntu, IconBrandWindows, IconRefresh, IconTrash, IconX } from '@tabler/icons-react'
import { useCallback, useEffect, useState } from 'react'
import * as api from '../../api'
import { useStore } from '../../store'
import type { AIProfile, Runner } from '../../types'
import './SpacesList.css'

function osIcon(os: string) {
  if (os === 'windows') return <IconBrandWindows size={14} />
  if (os === 'darwin') return <IconBrandApple size={14} />
  return <IconBrandUbuntu size={14} />
}

// Runners: the machines a project can work on. Clicking one opens its settings
// (the preconfigured AIs it offers); it never changes the active project.
export function RunnersList() {
  const [runners, setRunners] = useState<Runner[]>([])
  const [open, setOpen] = useState<Runner | null>(null)
  // Bumped by the server when a runner shows up or leaves: refetch right away.
  const peersVersion = useStore(s => s.peersVersion)

  useEffect(() => {
    const load = () => api.listRunners().then(setRunners).catch(() => {})
    load()
    const id = setInterval(load, 20_000)
    return () => clearInterval(id)
  }, [peersVersion])

  if (runners.length === 0) return null

  return (
    <div className="spaces-list">
      <div className="spaces-list__header">Runners</div>
      {runners.map(r => (
        <button
          key={r.id}
          className="space-item"
          onClick={() => setOpen(r)}
          title={`${r.hostname} · ${r.os}`}
        >
          <span className="space-item__icon">{osIcon(r.os)}</span>
          <span className="space-item__label">{r.self ? `${r.hostname} (this)` : r.hostname}</span>
        </button>
      ))}
      {open && <RunnerPanel runner={open} onClose={() => setOpen(null)} />}
    </div>
  )
}

function accountLine(p: AIProfile) {
  return [p.email, p.organization, p.plan].filter(Boolean).join(' · ')
}

function RunnerPanel({ runner, onClose }: { runner: Runner; onClose: () => void }) {
  const [profiles, setProfiles] = useState<AIProfile[]>([])
  const [name, setName] = useState('')
  const [dir, setDir] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const load = useCallback(() => {
    api.listAIProfiles(runner.id).then(setProfiles).catch(e => setError((e as Error).message))
  }, [runner.id])
  useEffect(load, [load])

  const run = async (fn: () => Promise<unknown>) => {
    setBusy(true)
    setError('')
    try {
      await fn()
      load()
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setBusy(false)
    }
  }

  const add = () =>
    run(async () => {
      await api.addAIProfile(runner.id, { name: name.trim(), claude_config_dir: dir.trim() })
      setName('')
      setDir('')
    })

  return (
    <div className="workspace-picker-overlay" onClick={onClose}>
      <div className="workspace-picker-modal" onClick={e => e.stopPropagation()}>
        <div className="workspace-picker__header">
          <h2>{runner.hostname} · Preconfigured AI</h2>
          <button className="workspace-picker__close" onClick={onClose} aria-label="Close runner settings">
            <IconX size={16} />
          </button>
        </div>

        <div className="ai-list">
          {profiles.length === 0 && (
            <div className="workspace-picker__message">No preconfigured AI on this runner yet.</div>
          )}
          {profiles.map(p => (
            <div key={p.id} className="ai-item">
              <div className="ai-item__main">
                <div className="ai-item__name">{p.name}</div>
                <div className="ai-item__account">{accountLine(p) || 'Account unknown'}</div>
                <div className="ai-item__dir">{p.claude_config_dir}</div>
                {p.error && <div className="ai-item__error">{p.error}</div>}
              </div>
              <button
                className="nav-panel__rail-btn"
                title="Refresh account"
                disabled={busy}
                onClick={() => run(() => api.refreshAIProfile(runner.id, p.id))}
              >
                <IconRefresh size={14} />
              </button>
              <button
                className="nav-panel__rail-btn"
                title="Remove"
                disabled={busy}
                onClick={() => run(() => api.deleteAIProfile(runner.id, p.id))}
              >
                <IconTrash size={14} />
              </button>
            </div>
          ))}
        </div>

        <div className="workspace-picker__name-input">
          <label>Name</label>
          <input className="input" value={name} onChange={e => setName(e.target.value)} placeholder="e.g. Work account" />
        </div>
        <div className="workspace-picker__name-input">
          <label>Claude config dir (on {runner.hostname})</label>
          <input
            className="input"
            value={dir}
            onChange={e => setDir(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && dir.trim() && add()}
            placeholder="C:\Users\you\.claude-other"
          />
        </div>
        {error && <div className="workspace-picker__message error">{error}</div>}
        <div className="workspace-picker__footer">
          <button className="btn btn--secondary" onClick={onClose}>Close</button>
          <button className="btn btn--primary" onClick={add} disabled={busy || !dir.trim()}>
            Add AI
          </button>
        </div>
      </div>
    </div>
  )
}
