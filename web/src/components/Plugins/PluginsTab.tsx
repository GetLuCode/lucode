import { useCallback, useEffect, useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { IconAlertTriangle, IconCheck, IconCopy, IconExternalLink, IconLoader2, IconLock, IconPlug, IconPuzzle, IconRefresh, IconServer2, IconSettings, IconTerminal2, IconTrash, IconUpload } from '@tabler/icons-react'
import * as api from '../../api'
import { httpStatusOf, pluginErrorMessage, pluginSettingsPath, pluginTargets, runnerPairing, runnerPolicyProblem, safeHttpUrl, targetsLabel } from '../../lib/plugins'
import { copyText } from '../../lib/copyText'
import { addWSMessageHandler, getWSConnection, sendWSMessage } from '../../hooks/useWSConnection'
import { useAuthStore } from '../../store/auth'
import { usePlugins } from '../../store/plugins'
import type { InstalledPlugin, PluginCatalogEntry, PluginInspection, PluginRunnerInfo, PluginSettingDef } from '../../types'
import { PluginDialog } from './PluginDialog'
import { PluginIcon } from './PluginIcon'
import { PluginInstallDialog } from './PluginInstallDialog'
import { PluginSettingsPagePane } from './PluginPage'
import './Plugins.css'

// Settings → Plugins. Everything that changes something needs an admin; everyone else sees the same
// list, read-only.
export function PluginsTab() {
  const installed = usePlugins(s => s.installed)
  const catalog = usePlugins(s => s.catalog)
  const available = usePlugins(s => s.available)
  const loaded = usePlugins(s => s.loaded)
  const load = usePlugins(s => s.load)
  const isAdmin = useAuthStore(s => s.isAdmin)
  const [refused, setRefused] = useState(false) // the hub said 403: its word beats the saved flag
  const readOnly = !isAdmin || refused

  const [inspection, setInspection] = useState<PluginInspection | null>(null)
  const [dragging, setDragging] = useState(false)
  const [inspecting, setInspecting] = useState(false)
  const [error, setError] = useState('')
  const fileRef = useRef<HTMLInputElement>(null)
  const dragDepth = useRef(0)

  useEffect(() => { void load() }, [load])

  const fail = useCallback((e: unknown) => {
    if (httpStatusOf(e) === 403) setRefused(true)
    else setError(pluginErrorMessage(e))
  }, [])

  const inspect = async (file: File | undefined) => {
    if (!file) return
    setError('')
    if (!/\.zip$/i.test(file.name)) { setError('A plugin is a .zip file with a plugin.json inside.'); return }
    setInspecting(true)
    try {
      setInspection(await api.inspectPlugin(file))
    } catch (e) {
      fail(e)
    } finally {
      setInspecting(false)
    }
  }

  const onDrop = (e: React.DragEvent) => {
    e.preventDefault()
    dragDepth.current = 0
    setDragging(false)
    if (!readOnly) void inspect(e.dataTransfer.files?.[0])
  }

  if (loaded && !available) {
    return (
      <div className="plugin-state">
        <IconPlug size={28} stroke={1.4} aria-hidden />
        <p className="pv-empty__title">Plugins are not available on this hub</p>
        <p className="pv-empty__text">It is running a version from before plugins. Update and restart the hub to use them.</p>
      </div>
    )
  }

  const notInstalled = catalog.filter(c => !c.installed)

  return (
    <div
      className={`plugins-tab${dragging ? ' plugins-tab--dragging' : ''}`}
      onDragEnter={e => { e.preventDefault(); dragDepth.current++; if (!readOnly) setDragging(true) }}
      onDragOver={e => e.preventDefault()}
      onDragLeave={() => { dragDepth.current = Math.max(0, dragDepth.current - 1); if (dragDepth.current === 0) setDragging(false) }}
      onDrop={onDrop}
    >
      {readOnly ? (
        <p className="plugin-readonly"><IconLock size={14} aria-hidden /> Only an administrator can install or change plugins. You can see what is installed.</p>
      ) : (
        <button className="plugin-drop" onClick={() => fileRef.current?.click()} disabled={inspecting}>
          {inspecting ? <IconLoader2 size={18} className="plugin-spin" aria-hidden /> : <IconUpload size={18} stroke={1.6} aria-hidden />}
          <span>
            <strong>{inspecting ? 'Reading the plugin…' : dragging ? 'Drop it to review' : 'Install a plugin'}</strong>
            <small>{inspecting ? 'Checking its contents and what it asks for' : 'Drop a .zip here, or choose a file. You review its permissions before anything is installed.'}</small>
          </span>
        </button>
      )}
      <input ref={fileRef} type="file" accept=".zip,application/zip" hidden onChange={e => { void inspect(e.target.files?.[0]); e.target.value = '' }} />
      {error && <p className="pv-error" role="alert"><IconAlertTriangle size={14} aria-hidden /><span>{error}</span></p>}

      <section className="plugins-tab__section">
        <h3 className="plugins-tab__title">Installed</h3>
        {!loaded && <div className="plugin-skeleton__block plugin-skeleton__block--row" aria-label="Loading" />}
        {loaded && installed.length === 0 && (
          <p className="plugins-tab__none">No plugins yet. {readOnly ? '' : 'Add one above, or start with the ones below.'}</p>
        )}
        <div className="plugin-rows">
          {installed.map(p => (
            <PluginRow
              key={p.manifest.id}
              plugin={p}
              readOnly={readOnly}
              onRefused={() => setRefused(true)}
            />
          ))}
        </div>
      </section>

      {notInstalled.length > 0 && (
        <section className="plugins-tab__section">
          <h3 className="plugins-tab__title">Included with lucode</h3>
          <div className="plugin-rows">
            {notInstalled.map(c => (
              <CatalogRow key={c.id} entry={c} readOnly={readOnly} onReview={setInspection} onFail={fail} />
            ))}
          </div>
        </section>
      )}

      {inspection && (
        <PluginInstallDialog inspection={inspection} onClose={() => setInspection(null)} onInstalled={() => setInspection(null)} />
      )}
    </div>
  )
}

function CatalogRow({ entry, readOnly, onReview, onFail }: { entry: PluginCatalogEntry; readOnly: boolean; onReview: (i: PluginInspection) => void; onFail: (e: unknown) => void }) {
  const [busy, setBusy] = useState(false)
  const install = async () => {
    setBusy(true)
    try {
      const answer = await api.installCatalogPlugin(entry.id)
      // A plugin with something to review comes back to the permission dialog, like an upload.
      if (answer && typeof (answer as PluginInspection).token === 'string') {
        onReview(answer as PluginInspection)
      } else {
        await usePlugins.getState().load()
        usePlugins.getState().toast(`${entry.name} installed`, 'success')
      }
    } catch (e) {
      onFail(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="plugin-row">
      <div className="plugin-row__main">
        <span className="plugin-tile"><IconPuzzle size={20} stroke={1.5} aria-hidden /></span>
        <div className="plugin-row__text">
          <span className="plugin-row__name">{entry.name}{entry.version && <span className="plugin-row__version">{entry.version}</span>}</span>
          {entry.description && <span className="plugin-row__desc">{entry.description}</span>}
        </div>
        {!readOnly && (
          <button className="pv-btn pv-btn--secondary" onClick={install} disabled={busy}>
            {busy && <IconLoader2 size={14} className="plugin-spin" aria-hidden />}
            Install
          </button>
        )}
      </div>
    </div>
  )
}

type Pane = 'settings' | 'runners' | 'logs' | 'about' | `page:${string}`

function PluginRow({ plugin, readOnly, onRefused }: { plugin: InstalledPlugin; readOnly: boolean; onRefused: () => void }) {
  const { manifest, enabled, status, error } = plugin
  const navigate = useNavigate()
  const location = useLocation()
  const [toggling, setToggling] = useState(false)
  const [rowError, setRowError] = useState('')
  const targets = pluginTargets(plugin)

  const setEnabled = async (on: boolean) => {
    setToggling(true)
    setRowError('')
    try {
      await api.setPluginEnabled(manifest.id, on)
      await usePlugins.getState().load()
    } catch (e) {
      if (httpStatusOf(e) === 403) onRefused()
      else setRowError(pluginErrorMessage(e))
    } finally {
      setToggling(false)
    }
  }

  const failed = status === 'error' || !!error
  const state = failed ? 'Error' : enabled ? 'On' : 'Off'

  return (
    <div className={`plugin-row${enabled ? '' : ' plugin-row--off'}`}>
      <div className="plugin-row__main">
        <span className="plugin-tile"><PluginIcon pluginId={manifest.id} path={manifest.icon} size={22} fallback={<IconPuzzle size={20} stroke={1.5} aria-hidden />} /></span>
        <div className="plugin-row__text">
          <span className="plugin-row__name">
            {manifest.name}
            <span className="plugin-row__version">{manifest.version}</span>
            <span className={`plugin-status plugin-status--${failed ? 'error' : enabled ? 'on' : 'off'}`}>{state}</span>
            <TargetBadge targets={targets} />
          </span>
          {manifest.description && <span className="plugin-row__desc">{manifest.description}</span>}
        </div>
        <PluginSwitch checked={enabled} busy={toggling} disabled={readOnly} label={`${manifest.name} enabled`} onChange={setEnabled} />
        <button
          className="plugin-icon-btn"
          onClick={() => navigate(pluginSettingsPath(manifest.id), { state: location.state })}
          aria-label={`${manifest.name} settings`}
          title="Settings"
        >
          <IconSettings size={16} aria-hidden />
        </button>
      </div>
      {error && <p className="pv-error plugin-row__error" role="alert"><IconAlertTriangle size={14} aria-hidden /><span>{error}</span></p>}
      {rowError && <p className="pv-error plugin-row__error" role="alert"><IconAlertTriangle size={14} aria-hidden /><span>{rowError}</span></p>}
    </div>
  )
}

// /settings/plugin/:pluginId — the plugin's own settings page: its fields, runners, logs and about.
export function PluginSettingsPage({ pluginId }: { pluginId: string }) {
  const installed = usePlugins(s => s.installed)
  const loaded = usePlugins(s => s.loaded)
  const load = usePlugins(s => s.load)
  const isAdmin = useAuthStore(s => s.isAdmin)
  const [refused, setRefused] = useState(false)
  const readOnly = !isAdmin || refused
  useEffect(() => { void load() }, [load])

  const plugin = installed.find(p => p.manifest.id === pluginId)
  if (!plugin) {
    return loaded
      ? <p className="plugins-tab__none">This plugin is not installed.</p>
      : <div className="plugin-skeleton__block plugin-skeleton__block--row" aria-label="Loading" />
  }
  return <PluginDetail key={pluginId} plugin={plugin} readOnly={readOnly} onRefused={() => setRefused(true)} />
}

function PluginDetail({ plugin, readOnly, onRefused }: { plugin: InstalledPlugin; readOnly: boolean; onRefused: () => void }) {
  const { manifest } = plugin
  const navigate = useNavigate()
  const targets = pluginTargets(plugin)
  const settings = manifest.contributes?.settings ?? []
  const [pane, setPane] = useState<Pane>(settings.length > 0 ? 'settings' : 'about')
  const [uninstalling, setUninstalling] = useState(false)
  // null: not asked yet. The tab appears only if the hub has the route.
  const [runners, setRunners] = useState<PluginRunnerInfo[] | null>(null)
  const [runnersError, setRunnersError] = useState('')
  const [runnersAbsent, setRunnersAbsent] = useState(false)
  const hasRunnerTarget = targets.includes('runner')

  const loadRunners = useCallback(() => {
    if (!hasRunnerTarget) return
    api.listPluginRunners(manifest.id)
      .then(r => { setRunners(r); setRunnersError('') })
      .catch(e => {
        if (httpStatusOf(e) === 404 || httpStatusOf(e) === 0) setRunnersAbsent(true) // an older hub: no section, no complaint
        else setRunnersError(pluginErrorMessage(e))
      })
  }, [hasRunnerTarget, manifest.id, setRunners, setRunnersError, setRunnersAbsent])
  useEffect(() => { loadRunners() }, [loadRunners])

  // pages the plugin put in its own settings (placement "settings"), e.g. its tests
  const pages = usePlugins(s => s.ui.pages).filter(p => p.pluginId === manifest.id && p.placement === 'settings')
  const panes: { id: Pane; label: string }[] = [
    ...(settings.length > 0 ? [{ id: 'settings' as const, label: 'Settings' }] : []),
    ...pages.map(p => ({ id: `page:${p.id}` as const, label: p.label })),
    ...(hasRunnerTarget && !runnersAbsent ? [{ id: 'runners' as const, label: 'Runners' }] : []),
    { id: 'logs', label: 'Logs' },
    { id: 'about', label: 'About' },
  ]
  const shown = panes.some(p => p.id === pane) ? pane : panes[0].id
  const blocked = (runners ?? []).some(r => runnerPolicyProblem(plugin, r))

  return (
    <div className="plugin-row__detail-inner">
      <div className="plugin-panes" role="tablist">
        {panes.map(p => (
          <button key={p.id} role="tab" aria-selected={shown === p.id} className={`plugin-pane${shown === p.id ? ' plugin-pane--on' : ''}`} onClick={() => setPane(p.id)}>
            {p.label}
            {p.id === 'runners' && blocked && <IconAlertTriangle size={12} className="plugin-pane__warn" aria-label="A runner blocks this plugin" />}
          </button>
        ))}
      </div>
      {shown === 'settings' && <PluginSettingsForm pluginId={manifest.id} defs={settings} readOnly={readOnly} onRefused={onRefused} />}
      {shown === 'runners' && <PluginRunners plugin={plugin} runners={runners} error={runnersError} readOnly={readOnly} onRefused={onRefused} onRefresh={loadRunners} />}
      {pages.map(p => shown === `page:${p.id}` && <PluginSettingsPagePane key={p.id} pluginId={manifest.id} page={p} />)}
      {shown === 'logs' && <PluginLogs pluginId={manifest.id} />}
      {shown === 'about' && <PluginAbout plugin={plugin} readOnly={readOnly} onUninstall={() => setUninstalling(true)} />}
      {uninstalling && <UninstallDialog plugin={plugin} onClose={() => setUninstalling(false)} onRefused={onRefused} onDone={() => navigate('/settings/plugins', { replace: true })} />}
    </div>
  )
}

function TargetBadge({ targets }: { targets: ReturnType<typeof pluginTargets> }) {
  const runner = targets.includes('runner')
  return (
    <span className={`plugin-target${runner ? ' plugin-target--runner' : ''}`} title={`Runs on: ${targetsLabel(targets).toLowerCase()}`}>
      {runner ? <IconTerminal2 size={11} aria-hidden /> : <IconServer2 size={11} aria-hidden />}
      {targetsLabel(targets)}
    </span>
  )
}

function PluginSwitch({ checked, busy, disabled, label, onChange }: { checked: boolean; busy: boolean; disabled: boolean; label: string; onChange: (on: boolean) => void }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      className={`plugin-switch${checked ? ' plugin-switch--on' : ''}${busy ? ' plugin-switch--busy' : ''}`}
      disabled={disabled || busy}
      title={disabled ? 'Only an administrator can change this' : undefined}
      onClick={() => onChange(!checked)}
    >
      <span className="plugin-switch__knob" />
    </button>
  )
}

const optionsOf = (d: PluginSettingDef) => (d.options ?? []).map(o => (typeof o === 'string' ? { value: o, label: o } : { value: o.value, label: o.label ?? o.value }))
const isSecretSet = (v: unknown) => !!v && typeof v === 'object' && (v as { set?: unknown }).set === true

function PluginSettingsForm({ pluginId, defs, readOnly, onRefused }: { pluginId: string; defs: PluginSettingDef[]; readOnly: boolean; onRefused: () => void }) {
  const [stored, setStored] = useState<Record<string, unknown> | null>(null)
  const [values, setValues] = useState<Record<string, unknown>>({})
  const [error, setError] = useState('')
  const [saving, setSaving] = useState(false)
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    let cancelled = false
    api.getPluginSettings(pluginId)
      .then(answer => { if (!cancelled) { setStored(answer ?? {}); setValues({}) } })
      .catch(e => { if (!cancelled) { setStored({}); setError(pluginErrorMessage(e)) } })
    return () => { cancelled = true }
  }, [pluginId])

  if (!stored) return <div className="plugin-skeleton__block plugin-skeleton__block--row" aria-label="Loading" />

  const current = (d: PluginSettingDef) => (d.key in values ? values[d.key] : d.type === 'secret' ? '' : stored[d.key] ?? d.default ?? '')
  const set = (key: string, v: unknown) => { setValues(prev => ({ ...prev, [key]: v })); setSaved(false) }
  const dirty = Object.keys(values).length > 0

  const save = async () => {
    setSaving(true)
    setError('')
    try {
      // an untouched secret is not sent: its value never came to this page
      const sent: Record<string, unknown> = {}
      for (const d of defs) {
        if (d.type === 'secret') { if (typeof values[d.key] === 'string' && values[d.key] !== '') sent[d.key] = values[d.key] }
        else sent[d.key] = current(d)
      }
      await api.savePluginSettings(pluginId, sent)
      const fresh = await api.getPluginSettings(pluginId)
      setStored(fresh ?? {})
      setValues({})
      setSaved(true)
    } catch (e) {
      if (httpStatusOf(e) === 403) onRefused()
      else setError(pluginErrorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  // The hub takes an empty value as "no value".
  const clearSecret = async (key: string) => {
    setSaving(true)
    setError('')
    try {
      await api.savePluginSettings(pluginId, { [key]: '' })
      setStored((await api.getPluginSettings(pluginId)) ?? {})
    } catch (e) {
      if (httpStatusOf(e) === 403) onRefused()
      else setError(pluginErrorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  return (
    <form className="pv-form" onSubmit={e => { e.preventDefault(); void save() }}>
      {defs.map(d => {
        const v = current(d)
        if (d.type === 'bool') {
          return (
            <label className="pv-check" key={d.key}>
              <input type="checkbox" checked={v === true} disabled={readOnly} onChange={e => set(d.key, e.target.checked)} />
              <span>{d.label}</span>
              {d.help && <small>{d.help}</small>}
            </label>
          )
        }
        return (
          <label className="pv-field" key={d.key}>
            <span>{d.label}</span>
            {d.type === 'textarea'
              ? <textarea className="input" rows={4} value={String(v)} disabled={readOnly} onChange={e => set(d.key, e.target.value)} />
              : d.type === 'select'
                ? (
                  <select className="input" value={String(v)} disabled={readOnly} onChange={e => set(d.key, e.target.value)}>
                    {optionsOf(d).map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
                  </select>
                )
                : (
                  <input
                    className="input"
                    type={d.type === 'number' ? 'number' : d.type === 'secret' ? 'password' : 'text'}
                    autoComplete={d.type === 'secret' ? 'off' : undefined}
                    placeholder={d.type === 'secret' ? (isSecretSet(stored[d.key]) ? 'Saved on the hub. Type to replace it.' : 'Not set') : undefined}
                    value={String(v)}
                    disabled={readOnly}
                    onChange={e => set(d.key, d.type === 'number' && e.target.value !== '' ? Number(e.target.value) : e.target.value)}
                  />
                )}
            {d.help && <small>{d.help}</small>}
            {d.type === 'secret' && !readOnly && isSecretSet(stored[d.key]) && (
              <button type="button" className="plugin-link-btn plugin-link-btn--quiet" disabled={saving} onClick={() => void clearSecret(d.key)}>Remove the saved value</button>
            )}
          </label>
        )
      })}
      {error && <p className="pv-error" role="alert"><IconAlertTriangle size={14} aria-hidden /><span>{error}</span></p>}
      {!readOnly && (
        <div className="pv-form__actions">
          {saved && !dirty && <span className="plugin-saved">Saved</span>}
          <button className="pv-btn pv-btn--primary" type="submit" disabled={saving || !dirty}>
            {saving && <IconLoader2 size={14} className="plugin-spin" aria-hidden />}
            Save settings
          </button>
        </div>
      )}
    </form>
  )
}

// The flag is what the operator has to change, so it reads as code in the sentence.
function WithFlags({ text }: { text: string }) {
  const parts = text.split(/(--plugin-policy=\w+)/)
  return <>{parts.map((p, i) => (i % 2 ? <code key={i}>{p}</code> : p))}</>
}

// Pairing gives a runner the secret it needs to take plugins from this hub. The code lives only in
// this dialog's state, so closing it forgets it. Pairing again replaces the secret, so it asks first.
function PairRunnerDialog({ runner, onClose, onRefused }: { runner: PluginRunnerInfo; onClose: () => void; onRefused: () => void }) {
  const name = runner.hostname || runner.runnerId
  const [command, setCommand] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [copied, setCopied] = useState(false)

  const pair = useCallback(async () => {
    setBusy(true)
    setError('')
    try {
      setCommand((await api.pairPluginRunner(runner.runnerId)).command)
    } catch (e) {
      if (httpStatusOf(e) === 403) { onRefused(); onClose(); return }
      setError(pluginErrorMessage(e))
    } finally {
      setBusy(false)
    }
  }, [runner.runnerId, onRefused, onClose])

  // A first pairing has nothing to lose, so the command is made at once.
  const first = !runner.paired
  const asked = useRef(false)
  useEffect(() => {
    if (first && !asked.current) {
      asked.current = true
      void pair()
    }
  }, [first, pair])

  const copy = () => {
    copyText(command)
    setCopied(true)
  }

  if (!command && !first) {
    return (
      <PluginDialog
        title={`Pair ${name} again?`}
        busy={busy}
        onClose={onClose}
        footer={<>
          <button className="pv-btn pv-btn--ghost" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="pv-btn pv-btn--danger" onClick={pair} disabled={busy}>
            {busy && <IconLoader2 size={14} className="plugin-spin" aria-hidden />}
            Pair again
          </button>
        </>}
      >
        <p className="plugin-dialog__text">This cuts off its current pairing. Plugins stop working on {name} until the new command has been run on it.</p>
        {error && <p className="pv-error" role="alert"><IconAlertTriangle size={14} aria-hidden /><span>{error}</span></p>}
      </PluginDialog>
    )
  }

  return (
    <PluginDialog title={`Pair ${name}`} wide busy={busy} onClose={onClose} footer={<button className="pv-btn pv-btn--primary" onClick={onClose} disabled={busy}>Done</button>}>
      {error && <p className="pv-error" role="alert"><IconAlertTriangle size={14} aria-hidden /><span>{error}</span></p>}
      {busy && !command && <div className="plugin-skeleton__block plugin-skeleton__block--row" aria-label="Loading" />}
      {command && (
        <>
          <p className="plugin-dialog__text">Run this on {name}, then restart its lucode-runner.</p>
          <div className="plugin-pair__command">
            <code>{command}</code>
            <button className="pv-btn pv-btn--secondary" onClick={copy}>
              {copied ? <IconCheck size={14} aria-hidden /> : <IconCopy size={14} aria-hidden />}
              {copied ? 'Copied' : 'Copy'}
            </button>
          </div>
          <p className="plugin-review__where">This is a secret: anyone who has it can pair a runner with this hub. It is not shown again. If you lose it, pair again.</p>
        </>
      )}
    </PluginDialog>
  )
}

function PluginRunners({ plugin, runners, error, readOnly, onRefused, onRefresh }: { plugin: InstalledPlugin; runners: PluginRunnerInfo[] | null; error: string; readOnly: boolean; onRefused: () => void; onRefresh: () => void }) {
  const [pairing, setPairing] = useState<PluginRunnerInfo | null>(null)
  if (error) return <p className="pv-error" role="alert"><IconAlertTriangle size={14} aria-hidden /><span>{error}</span></p>
  if (!runners) return <div className="plugin-skeleton__block plugin-skeleton__block--row" aria-label="Loading" />
  return (
    <div className="plugin-runners">
      <div className="plugin-logs__bar">
        <span className="plugin-logs__note">Where this plugin's runner code is delivered.</span>
        <button className="plugin-icon-btn" onClick={onRefresh} aria-label="Refresh" title="Refresh"><IconRefresh size={14} /></button>
      </div>
      {runners.length === 0 && <p className="plugins-tab__none">No runners are connected to this hub.</p>}
      <ul className="plugin-runner-list">
        {runners.map(r => {
          const problem = runnerPolicyProblem(plugin, r)
          return (
            <li key={r.runnerId} className="plugin-runner">
              <div className="plugin-runner__line">
                <span className={`plugin-dot${r.online ? ' plugin-dot--on' : ''}`} aria-hidden />
                <span className="plugin-runner__name">{r.hostname || r.runnerId}</span>
                <span className="plugin-runner__state">{r.online ? 'Online' : 'Offline'}</span>
                <span className="plugin-runner__fact">Policy <code>{r.policy || 'unknown'}</code></span>
                <span className="plugin-runner__fact">{r.delivered ? `Delivered${r.version ? ` · ${r.version}` : ''}` : 'Not delivered'}</span>
                {runnerPairing(r) === 'paired' && <span className="plugin-runner__fact">Paired</span>}
                {runnerPairing(r) === 'unpaired' && <span className="plugin-runner__unpaired">Not paired</span>}
                {runnerPairing(r) !== 'unknown' && !readOnly && (
                  <button className={`pv-btn ${r.paired ? 'pv-btn--ghost' : 'pv-btn--secondary'} plugin-runner__pair`} onClick={() => setPairing(r)}>
                    {r.paired ? 'Pair again' : 'Pair'}
                  </button>
                )}
              </div>
              {runnerPairing(r) === 'unpaired' && (
                <p className="plugins-tab__none">Plugins will not run on this runner until it is paired with this hub.{readOnly ? ' An administrator can pair it.' : ''}</p>
              )}
              {problem && <p className="pv-error pv-error--warning"><IconAlertTriangle size={14} aria-hidden /><span><WithFlags text={problem} /></span></p>}
              {r.error && <p className="pv-error" role="alert"><IconAlertTriangle size={14} aria-hidden /><span>{r.error}</span></p>}
            </li>
          )
        })}
      </ul>
      {pairing && <PairRunnerDialog runner={pairing} onRefused={onRefused} onClose={() => { setPairing(null); onRefresh() }} />}
    </div>
  )
}

const LIVE_LINES = 500 // as many as the hub keeps
interface LogLine { time: string; level: string; text: string }
function logLines(raw: unknown): LogLine[] {
  const list = Array.isArray(raw) ? raw : raw && typeof raw === 'object' && Array.isArray((raw as { logs?: unknown }).logs) ? (raw as { logs: unknown[] }).logs : []
  return list.map(item => {
    if (typeof item === 'string') return { time: '', level: '', text: item }
    const o = (item ?? {}) as Record<string, unknown>
    const said = o.message ?? o.msg ?? o.text
    return { time: String(o.time ?? o.ts ?? ''), level: String(o.level ?? ''), text: typeof said === 'string' ? said : JSON.stringify(said ?? '') }
  })
}

function PluginLogs({ pluginId }: { pluginId: string }) {
  const [lines, setLines] = useState<LogLine[] | null>(null)
  const [error, setError] = useState('')
  const [refused, setRefused] = useState(false)
  const load = useCallback(() => {
    api.getPluginLogs(pluginId)
      .then(raw => { setLines(logLines(raw)); setError('') })
      .catch(e => {
        if (httpStatusOf(e) === 403) setRefused(true)
        else setError(pluginErrorMessage(e))
      })
  }, [pluginId])
  useEffect(() => { load() }, [load])

  // Off by default: lines only travel while this switch is on, and only to this browser.
  const [live, setLive] = useState(false)
  useEffect(() => {
    if (!live) return
    const follow = (on: boolean) => sendWSMessage({ type: 'plugin_logs', plugin_id: pluginId, on })
    follow(true)
    const stop = addWSMessageHandler(e => {
      let ev: { type?: string; pluginId?: string } & Partial<LogLine>
      try { ev = JSON.parse(e.data as string) } catch { return }
      if (ev.type !== 'plugin_log' || ev.pluginId !== pluginId) return
      const line = { time: String(ev.time ?? ''), level: String(ev.level ?? ''), text: String(ev.text ?? '') }
      setLines(prev => [...(prev ?? []), line].slice(-LIVE_LINES))
    })
    // a new connection forgets who was following: ask again
    const again = () => follow(true)
    let ws: Awaited<ReturnType<typeof getWSConnection>> | null = null
    let closed = false
    void Promise.resolve(getWSConnection()).then(w => { if (closed) return; ws = w; w.addEventListener('open', again) })
    return () => {
      closed = true
      ws?.removeEventListener('open', again)
      stop()
      follow(false)
    }
  }, [live, pluginId])

  if (refused) return <p className="plugin-readonly"><IconLock size={14} aria-hidden /> Only an administrator can read a plugin's logs.</p>
  return (
    <div className="plugin-logs">
      <div className="plugin-logs__bar">
        <span className="plugin-logs__note">{live ? 'Following live.' : 'Since the hub last started.'}</span>
        <span className="plugin-logs__live">
          <span>Live</span>
          <PluginSwitch checked={live} busy={false} disabled={false} label="Follow the log live" onChange={on => { setLive(on); if (on) load() }} />
        </span>
        <button className="plugin-icon-btn" onClick={load} aria-label="Refresh" title="Refresh"><IconRefresh size={14} /></button>
      </div>
      {error && <p className="pv-error" role="alert"><IconAlertTriangle size={14} aria-hidden /><span>{error}</span></p>}
      {lines?.length === 0 && <p className="plugins-tab__none">Nothing logged yet.</p>}
      {!!lines?.length && (
        <div className="plugin-logs__list" tabIndex={0}>
          {lines.map((l, i) => (
            <div key={i} className={`plugin-log plugin-log--${l.level}`}>
              {l.time && <time>{shortTime(l.time)}</time>}
              {l.level && <span className="plugin-log__level">{l.level}</span>}
              <span className="plugin-log__text">{l.text}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

function shortTime(raw: string) {
  const d = new Date(raw)
  return Number.isNaN(d.getTime()) ? raw : d.toLocaleTimeString()
}

function PluginAbout({ plugin, readOnly, onUninstall }: { plugin: InstalledPlugin; readOnly: boolean; onUninstall: () => void }) {
  const { manifest, granted, source } = plugin
  const homepage = safeHttpUrl(manifest.homepage)
  return (
    <div className="plugin-about">
      <dl className="plugin-facts">
        <dt>Id</dt><dd><code>{manifest.id}</code></dd>
        <dt>Runs on</dt><dd>{targetsLabel(pluginTargets(plugin))}</dd>
        {manifest.author && <><dt>Author</dt><dd>{manifest.author}</dd></>}
        {source && <><dt>Source</dt><dd>{source}</dd></>}
        {homepage && <><dt>Homepage</dt><dd><a className="pv-link" href={homepage} target="_blank" rel="noreferrer noopener">{homepage}<IconExternalLink size={12} aria-hidden /></a></dd></>}
        <dt>Granted</dt>
        <dd>
          {granted?.length
            ? <span className="plugin-chips">{granted.map(g => <code key={g}>{g}</code>)}</span>
            : 'Nothing'}
        </dd>
      </dl>
      {!readOnly && (
        <div className="plugin-about__actions">
          <button className="pv-btn pv-btn--danger" onClick={onUninstall}><IconTrash size={14} aria-hidden />Uninstall</button>
        </div>
      )}
    </div>
  )
}

function UninstallDialog({ plugin, onClose, onRefused, onDone }: { plugin: InstalledPlugin; onClose: () => void; onRefused: () => void; onDone: () => void }) {
  const [keepData, setKeepData] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const go = async () => {
    setBusy(true)
    setError('')
    try {
      await api.uninstallPlugin(plugin.manifest.id, keepData)
      await usePlugins.getState().load()
      usePlugins.getState().toast(`${plugin.manifest.name} uninstalled`)
      onDone()
    } catch (e) {
      if (httpStatusOf(e) === 403) { onRefused(); onClose(); return }
      setError(pluginErrorMessage(e))
      setBusy(false)
    }
  }
  return (
    <PluginDialog
      title={`Uninstall ${plugin.manifest.name}?`}
      busy={busy}
      onClose={onClose}
      footer={<>
        <button className="pv-btn pv-btn--ghost" onClick={onClose} disabled={busy}>Cancel</button>
        <button className="pv-btn pv-btn--danger" onClick={go} disabled={busy}>
          {busy && <IconLoader2 size={14} className="plugin-spin" aria-hidden />}
          Uninstall
        </button>
      </>}
    >
      <p className="plugin-dialog__text">Its pages, models and locked flows go away. Copies of its flows stay, and show which nodes are missing.</p>
      <label className="pv-check">
        <input type="checkbox" checked={keepData} onChange={e => setKeepData(e.target.checked)} />
        <span>Keep its stored data</span>
        <small>So installing it again picks up where it left off.</small>
      </label>
      {error && <p className="pv-error" role="alert"><IconAlertTriangle size={14} aria-hidden /><span>{error}</span></p>}
    </PluginDialog>
  )
}
