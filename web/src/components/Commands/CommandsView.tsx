import { useCallback, useEffect, useMemo, useState } from 'react'
import { IconAlertTriangle, IconEdit, IconLoader2, IconPlayerPlay, IconPlayerStop, IconPlus, IconRefresh, IconTerminal2, IconTrash, IconX } from '@tabler/icons-react'
import { useStore } from '../../store'
import * as api from '../../api'
import { addWSMessageHandler } from '../../hooks/useWSConnection'
import { TerminalPane } from './TerminalPane'
import { envProblem, relDirProblem, stateLabel } from './commandsTypes'
import type { CommandList, CommandTarget, EnvVar, ProjectCommand, RunnerShell, TermStatus } from './commandsTypes'
import './Commands.css'

// The server answers errors as "METHOD path → 409: text"; show just the text.
function errorText(e: unknown): string {
  if (e instanceof SyntaxError) return 'The hub is older than this page and does not know the Commands tab yet: restart it.'
  const m = e instanceof Error ? e.message : String(e)
  return m.replace(/^.*?→ \d+: /, '').trim()
}

function dotClass(s?: TermStatus): string {
  if (!s) return 'cmds-dot'
  if (s.state === 'running') return 'cmds-dot cmds-dot--running'
  if (s.state === 'failed' || (s.exit_code ?? 0) !== 0) return 'cmds-dot cmds-dot--bad'
  return 'cmds-dot cmds-dot--done'
}

export function CommandsView() {
  const projectId = useStore(s => s.activeProjectId)
  const [list, setList] = useState<CommandList | null>(null)
  const [selected, setSelected] = useState<string | null>(null)
  const [editing, setEditing] = useState<ProjectCommand | 'new' | null>(null)
  const [busy, setBusy] = useState('') // command id with an order in flight
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    if (!projectId) return
    try {
      const l = await api.listProjectCommands<CommandList>(projectId)
      setList(l)
      setSelected(cur => (cur && l.commands.some(c => c.id === cur) ? cur : l.commands[0]?.id ?? null))
    } catch (e) { setError(errorText(e)) }
  }, [projectId])

  useEffect(() => { setList(null); setSelected(null); setError(''); void load() }, [load])

  // the runner reports every change of state; the hub passes it on
  useEffect(() => addWSMessageHandler((e: MessageEvent) => {
    if (typeof e.data !== 'string' || !e.data.includes('command')) return
    try {
      const m = JSON.parse(e.data) as { type: string; project_id: string; command_id?: string; status?: TermStatus }
      if (m.project_id !== projectId) return
      if (m.type === 'commands_changed') void load() // an agent created or changed one
      else if (m.type === 'command_status') {
        setList(l => l && { ...l, commands: l.commands.map(c => (c.id === m.command_id ? { ...c, status: m.status } : c)) })
      }
    } catch { /* not ours */ }
  }), [projectId, load])

  const current = useMemo(() => list?.commands.find(c => c.id === selected) ?? null, [list, selected])

  async function order(c: ProjectCommand, what: 'start' | 'stop' | 'restart') {
    if (!projectId) return
    setBusy(c.id); setError('')
    try {
      if (what !== 'start') await api.stopProjectCommand(projectId, c.id)
      if (what !== 'stop') await api.startProjectCommand(projectId, c.id)
      setSelected(c.id)
    } catch (e) { setError(errorText(e)) } finally { setBusy('') }
  }

  async function remove(c: ProjectCommand) {
    if (!projectId || !window.confirm(`Delete the command "${c.name}"?${c.status?.state === 'running' ? ' It is running and will be stopped.' : ''}`)) return
    try { await api.deleteProjectCommand(projectId, c.id); await load() } catch (e) { setError(errorText(e)) }
  }

  if (!projectId) return null
  const noRunner = list && list.targets.length === 0

  return (
    <div className="cmds">
      <aside className="cmds-side">
        <header className="cmds-side__head">
          <span className="cmds-side__title">Commands</span>
          <button className="cmds-btn cmds-btn--primary" onClick={() => setEditing('new')} disabled={!!noRunner}>
            <IconPlus size={14} /> New
          </button>
        </header>
        {!list && !error && <div className="cmds-empty"><IconLoader2 size={16} className="cmds-spin" /> Loading…</div>}
        {noRunner && <div className="cmds-note"><IconAlertTriangle size={14} /> {list.reason}</div>}
        {list && list.commands.length === 0 && !noRunner && (
          <div className="cmds-empty">
            <IconTerminal2 size={22} />
            <p>No commands yet.</p>
            <p className="cmds-dim">Define the ones you run often (a dev server, the tests) and start them from here. They run in a terminal on the project's runner.</p>
          </div>
        )}
        <ul className="cmds-list">
          {list?.commands.map(c => {
            const running = c.status?.state === 'running'
            return (
              <li key={c.id} className={`cmds-item${c.id === selected ? ' cmds-item--active' : ''}`}>
                <button className="cmds-item__main" onClick={() => setSelected(c.id)} title={stateLabel(c.status)}>
                  <span className={dotClass(c.status)} />
                  <span className="cmds-item__text">
                    <span className="cmds-item__name">{c.name}</span>
                    <span className="cmds-item__cmd">{c.command || 'interactive shell'}{c.cwd ? `  ·  ${c.cwd}` : ''}{c.shell ? `  ·  ${c.shell.startsWith('custom-') ? 'custom shell' : c.shell}` : ''}{c.env?.length ? `  ·  ${c.env.length} env` : ''}</span>
                  </span>
                </button>
                <span className="cmds-item__actions">
                  {busy === c.id ? <IconLoader2 size={15} className="cmds-spin" /> : running ? (
                    <>
                      <button className="cmds-icon" title="Restart" onClick={() => order(c, 'restart')}><IconRefresh size={15} /></button>
                      <button className="cmds-icon cmds-icon--stop" title="Stop" onClick={() => order(c, 'stop')}><IconPlayerStop size={15} /></button>
                    </>
                  ) : (
                    <button className="cmds-icon cmds-icon--run" title="Run" onClick={() => order(c, 'start')}><IconPlayerPlay size={15} /></button>
                  )}
                  <button className="cmds-icon" title="Edit" onClick={() => setEditing(c)}><IconEdit size={15} /></button>
                  <button className="cmds-icon cmds-icon--danger" title="Delete" onClick={() => remove(c)}><IconTrash size={15} /></button>
                </span>
              </li>
            )
          })}
        </ul>
      </aside>

      <section className="cmds-main">
        {error && (
          <div className="cmds-error" role="alert">
            <IconAlertTriangle size={14} /> <span>{error}</span>
            <button className="cmds-icon" onClick={() => setError('')} aria-label="Dismiss"><IconX size={14} /></button>
          </div>
        )}
        {current ? (
          <>
            <header className="cmds-bar">
              <span className={dotClass(current.status)} />
              <strong>{current.name}</strong>
              <span className="cmds-bar__state">{stateLabel(current.status)}</span>
              <span className="cmds-bar__cwd" title={current.status?.cwd}>{current.status?.cwd ?? ''}</span>
            </header>
            <div className="cmds-termwrap">
              <TerminalPane key={current.id} projectId={projectId} commandId={current.id} />
              {!current.status && (
                <div className="cmds-hint">
                  <p>Not started.</p>
                  <button className="cmds-btn cmds-btn--primary" onClick={() => order(current, 'start')}><IconPlayerPlay size={14} /> Run</button>
                </div>
              )}
            </div>
          </>
        ) : (
          <div className="cmds-empty cmds-empty--center"><IconTerminal2 size={28} /><p>Pick a command to see its terminal.</p></div>
        )}
      </section>

      {editing && list && (
        <CommandForm
          projectId={projectId}
          initial={editing === 'new' ? null : editing}
          targets={list.targets}
          onClose={() => setEditing(null)}
          onSaved={async id => { setEditing(null); await load(); setSelected(id) }}
        />
      )}
    </div>
  )
}

function CommandForm({ projectId, initial, targets, onClose, onSaved }: {
  projectId: string; initial: ProjectCommand | null; targets: CommandTarget[]; onClose: () => void; onSaved: (id: string) => void
}) {
  const [name, setName] = useState(initial?.name ?? '')
  const [command, setCommand] = useState(initial?.command ?? '')
  const [cwd, setCwd] = useState(initial?.cwd ?? '')
  const [runner, setRunner] = useState(initial?.runner_id ?? '')
  const [shell, setShell] = useState(initial?.shell ?? '')
  const [env, setEnv] = useState<EnvVar[]>(initial?.env ?? [])
  const [shells, setShells] = useState<RunnerShell[] | null>(null) // null = loading
  const [shellsError, setShellsError] = useState('')
  const [registering, setRegistering] = useState(false)
  const [reg, setReg] = useState({ name: '', path: '', args: '' })
  const [regBusy, setRegBusy] = useState(false)
  const [regError, setRegError] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const runners = useMemo(() => {
    const seen = new Map<string, CommandTarget>()
    for (const t of targets) if (!seen.has(t.runner_id)) seen.set(t.runner_id, t)
    return [...seen.values()]
  }, [targets])
  const effective = runner || targets[0]?.runner_id || ''
  const base = targets.find(t => t.runner_id === effective)?.repo ?? ''
  const dirProblem = relDirProblem(cwd)
  const envIssue = envProblem(env)

  // The shells belong to the runner the command will run on: ask it again when that changes.
  const loadShells = useCallback(async () => {
    setShells(null); setShellsError('')
    try {
      const r = await api.listCommandShells<{ shells: RunnerShell[] }>(projectId, effective || undefined)
      setShells(r.shells)
    } catch (e) { setShells([]); setShellsError(errorText(e)) }
  }, [projectId, effective])
  useEffect(() => { void loadShells() }, [loadShells])

  const shellKnown = !shell || !shells || shells.some(s => s.id === shell)
  const chosen = shells?.find(s => s.id === shell)

  async function registerShell() {
    if (regBusy) return
    setRegBusy(true); setRegError('')
    try {
      const args = reg.args.trim() ? reg.args.trim().split(/\s+/) : []
      const saved = await api.addCommandShell<RunnerShell>(projectId, effective || undefined, { name: reg.name, path: reg.path, args })
      await loadShells()
      setShell(saved.id)
      setRegistering(false); setReg({ name: '', path: '', args: '' })
    } catch (e) { setRegError(errorText(e)) } finally { setRegBusy(false) }
  }

  async function forgetShell(id: string) {
    if (!window.confirm('Forget this shell on the runner? Commands that use it will not start until they pick another.')) return
    try { await api.deleteCommandShell(projectId, effective || undefined, id); setShell(''); await loadShells() } catch (e) { setShellsError(errorText(e)) }
  }

  const setVar = (i: number, patch: Partial<EnvVar>) => setEnv(list => list.map((v, j) => (j === i ? { ...v, ...patch } : v)))

  async function save(e: React.FormEvent) {
    e.preventDefault()
    if (!name.trim() || dirProblem || envIssue) return
    setSaving(true); setError('')
    try {
      const saved = await api.saveProjectCommand<ProjectCommand>(projectId, initial?.id ?? null, { name, command, cwd: cwd.trim(), runner_id: runner, shell, env: env.filter(v => v.name.trim() || v.value) })
      onSaved(saved.id)
    } catch (err) { setError(errorText(err)); setSaving(false) }
  }

  return (
    <div className="cmds-overlay" onMouseDown={e => { if (e.target === e.currentTarget) onClose() }}>
      <form className="cmds-form" onSubmit={save} role="dialog" aria-label={initial ? 'Edit command' : 'New command'}>
        <h3>{initial ? 'Edit command' : 'New command'}</h3>
        <label>
          Name
          <input autoFocus value={name} onChange={e => setName(e.target.value)} placeholder="Dev server" />
        </label>
        <label>
          Command
          <textarea rows={3} value={command} onChange={e => setCommand(e.target.value)} spellCheck={false}
            placeholder="pnpm dev" className="cmds-mono" />
          <span className="cmds-dim">Leave it empty to open an interactive shell.</span>
        </label>
        <label>
          Folder
          <input value={cwd} onChange={e => setCwd(e.target.value)} placeholder="web  (empty = the work directory)" className="cmds-mono" spellCheck={false} />
          <span className={dirProblem ? 'cmds-bad' : 'cmds-dim'}>
            {dirProblem || (base ? <>Relative to <code>{base}</code> on the runner.</> : 'Relative to the project\'s work directory.')}
          </span>
        </label>
        {runners.length > 1 && (
          <label>
            Runner
            <select value={runner} onChange={e => setRunner(e.target.value)}>
              <option value="">The project's runner</option>
              {runners.map(r => <option key={r.runner_id} value={r.runner_id}>{r.hostname || r.runner_id}{r.online ? '' : ' (offline)'}</option>)}
            </select>
          </label>
        )}
        <label>
          Shell
          <select value={shell} onChange={e => setShell(e.target.value)} disabled={shells === null}>
            <option value="">The runner's default</option>
            {!shellKnown && <option value={shell}>{shell} (not on this runner)</option>}
            {shells?.map(s => <option key={s.id} value={s.id}>{s.name}{s.builtin ? '' : ' (registered)'}</option>)}
          </select>
          <span className={shellsError || !shellKnown ? 'cmds-bad' : 'cmds-dim'}>
            {shellsError || (!shellKnown ? 'This runner does not have that shell: pick another or register it.' : chosen ? <code>{chosen.path}</code> : 'The shells come from the runner the command runs on.')}
          </span>
          <span className="cmds-shellbar">
            <button type="button" className="cmds-btn" onClick={() => setRegistering(v => !v)}>{registering ? 'Cancel' : 'Register a shell…'}</button>
            {chosen && !chosen.builtin && <button type="button" className="cmds-btn" onClick={() => forgetShell(chosen.id)}>Forget this shell</button>}
          </span>
        </label>
        {registering && (
          <fieldset className="cmds-reg" onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); void registerShell() } }}>
            <legend>Register a shell on the runner</legend>
            <label>Name<input value={reg.name} onChange={e => setReg({ ...reg, name: e.target.value })} placeholder="Zsh (Homebrew)" /></label>
            <label>Path on the runner<input value={reg.path} onChange={e => setReg({ ...reg, path: e.target.value })} className="cmds-mono" spellCheck={false} placeholder="/usr/local/bin/zsh" /></label>
            <label>Arguments before the command <span className="cmds-dim">(optional)</span>
              <input value={reg.args} onChange={e => setReg({ ...reg, args: e.target.value })} className="cmds-mono" spellCheck={false} placeholder="-c  (empty = the usual ones for that shell)" />
            </label>
            {regError && <span className="cmds-bad">{regError}</span>}
            <button type="button" className="cmds-btn cmds-btn--primary" disabled={regBusy || !reg.name.trim() || !reg.path.trim()} onClick={() => void registerShell()}>
              {regBusy ? <IconLoader2 size={14} className="cmds-spin" /> : null} Register on the runner
            </button>
          </fieldset>
        )}
        <fieldset className="cmds-env">
          <legend>Environment variables</legend>
          {env.length === 0 && <span className="cmds-dim">None. They are added to the runner's own environment when the command starts.</span>}
          {env.map((v, i) => (
            <div className="cmds-env__row" key={i}>
              <input value={v.name} onChange={e => setVar(i, { name: e.target.value })} className="cmds-mono" spellCheck={false} placeholder="NAME" aria-label="Variable name" />
              <input value={v.value} onChange={e => setVar(i, { value: e.target.value })} className="cmds-mono" spellCheck={false} placeholder="value" aria-label="Variable value" />
              <button type="button" className="cmds-icon cmds-icon--danger" title="Remove" onClick={() => setEnv(list => list.filter((_, j) => j !== i))}><IconTrash size={14} /></button>
            </div>
          ))}
          {envIssue && <span className="cmds-bad">{envIssue}</span>}
          <button type="button" className="cmds-btn" onClick={() => setEnv(list => [...list, { name: '', value: '' }])}><IconPlus size={14} /> Add variable</button>
          <span className="cmds-dim">Values are saved on the hub and sent to the runner: do not put secrets here that you would not keep there.</span>
        </fieldset>
        {error && <div className="cmds-error"><IconAlertTriangle size={14} /> <span>{error}</span></div>}
        <footer>
          <button type="button" className="cmds-btn" onClick={onClose}>Cancel</button>
          <button type="submit" className="cmds-btn cmds-btn--primary" disabled={saving || !name.trim() || !!dirProblem || !!envIssue}>
            {saving ? <IconLoader2 size={14} className="cmds-spin" /> : null} Save
          </button>
        </footer>
      </form>
    </div>
  )
}
