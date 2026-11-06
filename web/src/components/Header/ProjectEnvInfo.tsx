import { useEffect, useState } from 'react'
import * as api from '../../api'
import { IconX } from '@tabler/icons-react'
import { backdropClose } from '../../lib/backdropClose'
import type { AIProfile, Project, Runner } from '../../types'

// Modal opened by the "Info" entry of the project menu: shows where the project's `claude -p`
// turns run (runner, working directory, account / config dir), and therefore
// which CLAUDE.md files they load. An agent may override the runner or the AI.
// Mounted only while open, so it loads its data on mount.
export function ProjectEnvInfo({ project, onClose }: { project: Project; onClose: () => void }) {
  const [runners, setRunners] = useState<Runner[]>([])
  const [profiles, setProfiles] = useState<AIProfile[]>([])
  const runnerId = project.runner_id ?? ''

  useEffect(() => {
    api.listRunners().then(setRunners).catch(() => setRunners([]))
    api.listAIProfiles(runnerId || 'local').then(setProfiles).catch(() => setProfiles([]))
  }, [runnerId])

  const runner = runners.find(r => (r.self ? '' : r.id) === runnerId)
  const runnerLabel = runner ? `${runner.hostname}${runner.self ? ' (this)' : ''}` : runnerId || 'this instance'
  const dirs = runnerId ? project.env_dirs?.[runnerId] ?? [] : project.work_dirs
  const profile = profiles.find(p => p.id === project.ai_profile_id)
  const configDir = profile?.claude_config_dir || (runnerId ? '' : project.claude_config_dir) || ''
  const sep = (runner?.os ?? '').toLowerCase().includes('windows') || (!runner && /^[A-Za-z]:|\\/.test(dirs[0] ?? '')) ? '\\' : '/'
  const join = (d: string, f: string) => d.replace(/[\\/]+$/, '') + sep + f

  const rows: [string, string][] = [
    ['Runner', runnerLabel],
    ['Working dir', dirs[0] || '—'],
    ['AI', profile ? `${profile.name}${profile.email ? ` — ${profile.email}` : ''}` : 'Default account'],
    ['Config dir', configDir || 'default (~/.claude)'],
  ]
  if (dirs.length > 1) rows.splice(2, 0, ['Extra dirs', dirs.slice(1).join(', ')])

  return (
    <div className="workspace-picker-overlay" {...backdropClose(onClose)}>
      <div className="workspace-picker-modal" onClick={e => e.stopPropagation()} style={{ fontSize: 13 }}>
        <div className="workspace-picker__header">
          <h2>{project.name} · Info</h2>
          <button className="workspace-picker__close" onClick={onClose} aria-label="Close">
            <IconX size={16} />
          </button>
        </div>
        <div style={{ fontWeight: 600, marginBottom: 8 }}>Where claude -p runs</div>
        <dl style={{ margin: 0, display: 'grid', gridTemplateColumns: 'auto 1fr', gap: '4px 12px' }}>
          {rows.map(([k, v]) => (
            <div key={k} style={{ display: 'contents' }}>
              <dt style={{ opacity: 0.6 }}>{k}</dt>
              <dd style={{ margin: 0, wordBreak: 'break-all' }}>{v}</dd>
            </div>
          ))}
        </dl>
        <div style={{ fontWeight: 600, margin: '10px 0 4px' }}>CLAUDE.md loaded</div>
        <ul style={{ margin: 0, paddingLeft: 16, wordBreak: 'break-all' }}>
          <li>{configDir ? join(configDir, 'CLAUDE.md') : '~/.claude/CLAUDE.md'} (user)</li>
          {dirs[0] && <li>{join(dirs[0], 'CLAUDE.md')} (project, plus parent folders)</li>}
        </ul>
        <div style={{ opacity: 0.6, marginTop: 8 }}>An agent can override the runner or the AI. Change this in Environment…</div>
      </div>
    </div>
  )
}
