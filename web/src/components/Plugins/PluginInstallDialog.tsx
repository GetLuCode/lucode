import { useMemo, useState } from 'react'
import { IconAlertTriangle, IconArrowRight, IconCheck, IconExternalLink, IconPuzzle, IconServer2, IconTerminal2, IconLoader2 } from '@tabler/icons-react'
import * as api from '../../api'
import { effectiveRisk, groupPermissionsByTarget, isBrowserPermission, missingGrants, newPermissionKeys, pluginErrorMessage, safeHttpUrl, sortByRisk } from '../../lib/plugins'
import { usePlugins } from '../../store/plugins'
import type { PluginInspection, PluginPermission } from '../../types'
import { PluginDialog } from './PluginDialog'

const RISK_LABEL: Record<string, string> = { high: 'High risk', medium: 'Medium risk', low: 'Low risk' }

// The review before anything is installed: each permission the plugin asks for, what it allows,
// and how risky it is, grouped by where it applies. Nothing installs until all are ticked.
export function PluginInstallDialog({ inspection, onClose, onInstalled }: { inspection: PluginInspection; onClose: () => void; onInstalled: () => void }) {
  const { manifest, upgradeFrom, warnings } = inspection
  const permissions = useMemo(() => inspection.permissions ?? [], [inspection.permissions])
  const fresh = useMemo(() => newPermissionKeys(inspection.newPermissions), [inspection.newPermissions])
  // On an upgrade, what was already granted stays ticked: only what is new needs a decision.
  const [granted, setGranted] = useState<Set<string>>(() =>
    new Set(upgradeFrom ? permissions.filter(p => !fresh.has(p.key)).map(p => p.key) : []))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const missing = missingGrants(permissions, granted)
  const groups = groupPermissionsByTarget(permissions)
  const hasHigh = permissions.some(p => effectiveRisk(p) === 'high')
  const homepage = safeHttpUrl(manifest.homepage)
  const verb = upgradeFrom ? 'Upgrade' : 'Install'

  const toggle = (key: string, on: boolean) => setGranted(prev => {
    const next = new Set(prev)
    if (on) next.add(key)
    else next.delete(key)
    return next
  })
  // High risk always takes its own tick.
  const grantRest = () => setGranted(prev => new Set([...prev, ...permissions.filter(p => effectiveRisk(p) !== 'high').map(p => p.key)]))

  const install = async () => {
    setBusy(true)
    setError('')
    try {
      await api.installPlugin(inspection.token, [...granted])
      await usePlugins.getState().load()
      usePlugins.getState().toast(`${manifest.name} ${upgradeFrom ? 'upgraded' : 'installed'}`, 'success')
      onInstalled()
    } catch (e) {
      setError(pluginErrorMessage(e))
      setBusy(false)
    }
  }

  return (
    <PluginDialog
      wide
      busy={busy}
      title={`${verb} ${manifest.name}`}
      icon={<IconPuzzle size={20} stroke={1.6} aria-hidden />}
      onClose={onClose}
      subtitle={
        <span className="plugin-review__meta">
          {upgradeFrom
            ? <span className="plugin-review__upgrade">{upgradeFrom}<IconArrowRight size={12} aria-label="to" />{manifest.version}</span>
            : <span>{manifest.version}</span>}
          {manifest.author && <span>{manifest.author}</span>}
          <code>{manifest.id}</code>
          {homepage && <a href={homepage} target="_blank" rel="noreferrer noopener">Homepage<IconExternalLink size={11} aria-hidden /></a>}
        </span>
      }
      footer={
        <>
          <span className="plugin-review__status" role="status">
            {missing.length > 0 ? `Tick ${missing.length} more to continue` : permissions.length > 0 ? 'All permissions granted' : ''}
          </span>
          <button className="pv-btn pv-btn--ghost" onClick={onClose} disabled={busy}>Cancel</button>
          <button className="pv-btn pv-btn--primary" onClick={install} disabled={busy || missing.length > 0}>
            {busy && <IconLoader2 size={14} className="plugin-spin" aria-hidden />}
            {permissions.length > 0 ? `Grant and ${verb.toLowerCase()}` : verb}
          </button>
        </>
      }
    >
      {manifest.description && <p className="plugin-dialog__text">{manifest.description}</p>}

      {upgradeFrom && (
        <p className="plugin-review__note">
          {fresh.size > 0
            ? `This version asks for ${fresh.size === 1 ? 'one new permission' : `${fresh.size} new permissions`}. They are marked below and start unticked.`
            : 'This version asks for nothing new.'}
        </p>
      )}

      {(warnings ?? []).map((w, i) => (
        <p className="pv-error pv-error--warning" key={i}><IconAlertTriangle size={14} aria-hidden /><span>{w}</span></p>
      ))}

      {permissions.length === 0 ? (
        <p className="plugin-review__none"><IconCheck size={15} aria-hidden /> This plugin asks for no permissions.</p>
      ) : (
        <>
          <div className="plugin-review__head">
            <h3>It asks for</h3>
            {permissions.some(p => effectiveRisk(p) !== 'high' && !granted.has(p.key)) && (
              <button className="plugin-link-btn" onClick={grantRest}>{hasHigh ? 'Tick all except high risk' : 'Tick all'}</button>
            )}
          </div>
          {groups.map(g => (
            <section className="plugin-review__group" key={g.target}>
              <h4>
                {g.target === 'runner' ? <IconTerminal2 size={14} aria-hidden /> : <IconServer2 size={14} aria-hidden />}
                {g.label}
              </h4>
              {g.target === 'runner' && (
                <p className="plugin-review__where">Runs on the machines that hold your code and credentials. Each runner's operator policy can still refuse these.</p>
              )}
              <ul className="plugin-perms">
                {sortForReview(g.items, fresh).map(p => (
                  <PermissionRow key={p.key} permission={p} isNew={fresh.has(p.key)} checked={granted.has(p.key)} disabled={busy} onChange={on => toggle(p.key, on)} />
                ))}
              </ul>
            </section>
          ))}
        </>
      )}
      {error && <p className="pv-error" role="alert"><IconAlertTriangle size={14} aria-hidden /><span>{error}</span></p>}
    </PluginDialog>
  )
}

const sortForReview = (items: PluginPermission[], fresh: Set<string>) =>
  [...sortByRisk(items)].sort((a, b) => Number(fresh.has(b.key)) - Number(fresh.has(a.key)))

function PermissionRow({ permission, isNew, checked, disabled, onChange }: { permission: PluginPermission; isNew: boolean; checked: boolean; disabled: boolean; onChange: (on: boolean) => void }) {
  const risk = effectiveRisk(permission)
  return (
    <li className={`plugin-perm${checked ? ' plugin-perm--on' : ''}${risk === 'high' ? ' plugin-perm--high' : ''}`}>
      <label>
        <input type="checkbox" checked={checked} disabled={disabled} onChange={e => onChange(e.target.checked)} />
        <span className="plugin-perm__body">
          <span className="plugin-perm__line">
            <span className="plugin-perm__label">{permission.label || permission.key}</span>
            {isNew && <span className="pv-badge pv-badge--accent">New</span>}
            <span className={`plugin-risk plugin-risk--${risk}`}>
              {risk === 'high' && <IconAlertTriangle size={11} aria-hidden />}
              {RISK_LABEL[risk] ?? risk}
            </span>
          </span>
          {permission.detail && <span className="plugin-perm__detail">{permission.detail}</span>}
          {isBrowserPermission(permission.key) && (
            <span className="plugin-perm__detail">The origin limits where it opens pages. Scripts it runs in those pages can send what they read to any address, and it can use whatever account is signed in there.</span>
          )}
        </span>
      </label>
    </li>
  )
}
