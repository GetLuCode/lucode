import { useCallback, useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { IconBrandApple, IconBrandUbuntu, IconBrandWindows, IconRefresh, IconSettings } from '@tabler/icons-react'
import * as api from '../../api'
import { useStore } from '../../store'
import type { ClaudeCommand, Runner, RunnerVersion } from '../../types'
import { commandSummary } from '../../lib/commandLine'
import { RunnerPanel } from '../Sidebar/RunnersList'
import { CommandDetails } from './CommandDetails'

function osIcon(os: string) {
  if (os === 'windows') return <IconBrandWindows size={16} />
  if (os === 'darwin') return <IconBrandApple size={16} />
  return <IconBrandUbuntu size={16} />
}

const when = (iso?: string) => {
  if (!iso) return '—'
  const d = new Date(iso)
  return isNaN(d.getTime()) || d.getFullYear() < 2000 ? '—' : d.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

// Runners: the machines that execute claude. The hub does not, so it is not listed.
// Picking one shows what it is running and the commands it launched.
export function RunnersView() {
  const { projectId, runnerId } = useParams()
  const navigate = useNavigate()
  const peersVersion = useStore(s => s.peersVersion)
  const [runners, setRunners] = useState<Runner[] | null>(null)

  useEffect(() => {
    const load = () => api.listRunners().then(rs => setRunners(rs.filter(r => r.runner_only))).catch(() => setRunners([]))
    load()
    const id = setInterval(load, 15_000)
    return () => clearInterval(id)
  }, [peersVersion])

  const selected = runners?.find(r => r.id === runnerId)

  return (
    <div className="runners">
      <aside className="runners__list">
        <h2 className="runners__title">Runners</h2>
        {runners === null && <p className="kmodal__empty-hint">Loading…</p>}
        {runners?.length === 0 && <p className="kmodal__empty-hint">No runner is connected. Start a <code>lucode-runner</code> on a machine that points to this hub's NATS.</p>}
        {runners?.map(r => (
          <button
            key={r.id}
            className={`runner-card${r.id === runnerId ? ' runner-card--active' : ''}`}
            onClick={() => navigate(`/projects/${projectId}/runners/${r.id}`)}
          >
            <span className="runner-card__host">{osIcon(r.os)} {r.hostname}</span>
            <span className="runner-card__line">
              <span className="runner-card__ver" title="Build it was compiled from">{r.version || 'unknown version'}</span>
              <span>{r.os}</span>
            </span>
            <span className="runner-card__line runner-card__id" title={r.id}>{r.id.slice(0, 8)} · up since {when(r.started_at)}</span>
          </button>
        ))}
      </aside>

      <section className="runners__detail">
        {!selected && runners && runners.length > 0 && <p className="kmodal__empty-hint">Choose a runner to see the commands it launched.</p>}
        {selected && <RunnerDetail key={selected.id} runner={selected} />}
      </section>
    </div>
  )
}

function RunnerDetail({ runner }: { runner: Runner }) {
  const sessions = useStore(s => s.sessions)
  const [version, setVersion] = useState<RunnerVersion | null>(null)
  const [cmds, setCmds] = useState<ClaudeCommand[] | null>(null)
  const [error, setError] = useState('')
  const [openId, setOpenId] = useState('')
  const [aiOpen, setAiOpen] = useState(false)

  const load = useCallback(() => {
    api.getRunnerVersion(runner.id).then(setVersion).catch(() => setVersion(null))
    api.listRunnerCommands(runner.id, 200).then(c => { setCmds(c); setError('') }).catch(e => {
      setCmds(c => c ?? [])
      setError((e instanceof Error ? e.message : String(e)).replace(/^.*?→ \d+: /, ''))
    })
  }, [runner.id])

  useEffect(() => {
    load()
    const id = setInterval(load, 10_000)
    return () => clearInterval(id)
  }, [load])

  const sessionName = (id: string) => sessions.find(s => s.id === id)?.name || id.slice(0, 8)

  return (
    <div className="runner-detail">
      <div className="runner-detail__head">
        <div>
          <h2 className="runners__title">{runner.hostname}</h2>
          <dl className="cmd__meta">
            <dt>Version</dt>
            <dd className="cmd__mono">
              {version ? version.short : runner.version || '—'}
              {version?.commit_time && <span className="runner-detail__sub"> · commit {when(version.commit_time)}</span>}
            </dd>
            <dt>Built</dt><dd>{when(version?.built_at)}</dd>
            <dt>Running since</dt><dd>{when(version?.started_at ?? runner.started_at)}</dd>
            <dt>Platform</dt><dd>{version ? `${version.os}/${version.arch} · ${version.go}` : runner.os}</dd>
            <dt>Id</dt><dd className="cmd__mono">{runner.id}</dd>
          </dl>
        </div>
        <div className="runner-detail__actions">
          <button className="kmodal__btn kmodal__btn--cancel" onClick={load} title="Refresh"><IconRefresh size={13} /> Refresh</button>
          <button className="kmodal__btn kmodal__btn--cancel" onClick={() => setAiOpen(true)}><IconSettings size={13} /> Preconfigured AI</button>
        </div>
      </div>

      <h3 className="runner-detail__sub-title">Commands launched {cmds ? `(${cmds.length})` : ''}</h3>
      {error && <p className="review__msg review__msg--bad">{error}</p>}
      {cmds === null && !error && <p className="kmodal__empty-hint">Loading…</p>}
      {cmds?.length === 0 && !error && <p className="kmodal__empty-hint">This runner has not launched any command since it started recording them.</p>}

      <div className="runner-cmds">
        {cmds?.map(c => (
          <div key={c.id} className={`runner-cmd${c.error ? ' runner-cmd--failed' : ''}`}>
            <button className="runner-cmd__row" onClick={() => setOpenId(id => (id === c.id ? '' : c.id))}>
              <span className="runner-cmd__time">{when(c.at)}</span>
              <span className="runner-cmd__session" title={c.session_id}>{sessionName(c.session_id)}</span>
              <span className="runner-cmd__sum cmd__mono">{commandSummary(c)}</span>
              {c.error && <span className="runner-cmd__badge">failed</span>}
            </button>
            {openId === c.id && <CommandDetails cmd={c} />}
          </div>
        ))}
      </div>

      {aiOpen && <RunnerPanel runner={runner} onClose={() => setAiOpen(false)} />}
    </div>
  )
}
