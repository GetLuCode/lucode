import { useEffect, useState } from 'react'
import { IconBell, IconCheck, IconCopy } from '@tabler/icons-react'
import * as api from '../../api'
import type { Alert } from '../../types'

// Bell with the things lucode noticed and leaves for the user to clean up (today: merged
// worktrees left behind). It only reports: each alert carries the command to run.
export function AlertsBell({ projectId }: { projectId: string }) {
  const [alerts, setAlerts] = useState<Alert[]>([])
  const [open, setOpen] = useState(false)
  const [copied, setCopied] = useState('')

  useEffect(() => {
    let live = true
    const load = () => api.listProjectAlerts(projectId).then(a => live && setAlerts(a)).catch(() => {})
    setAlerts([])
    load()
    const id = setInterval(load, 60_000)
    return () => { live = false; clearInterval(id) }
  }, [projectId])

  async function copy(a: Alert) {
    try {
      await navigator.clipboard.writeText(a.command)
      setCopied(a.id)
      setTimeout(() => setCopied(c => (c === a.id ? '' : c)), 1500)
    } catch { /* clipboard blocked: the command is shown in the alert anyway */ }
  }

  return (
    <div className="header-menu">
      <button
        className={`header-icon-btn${open ? ' header-icon-btn--active' : ''}`}
        onClick={() => setOpen(v => !v)}
        title={alerts.length ? `${alerts.length} alert(s)` : 'No alerts'}
        aria-label="Alerts"
        aria-expanded={open}
      >
        <IconBell size={16} />
        {alerts.length > 0 && <span className="header-icon-btn__badge">{alerts.length}</span>}
      </button>
      {open && (
        <>
          <div className="header-menu__backdrop" onClick={() => setOpen(false)} />
          <div className="nav-tree__dropdown header-menu__dropdown alerts-dropdown" role="dialog" aria-label="Alerts">
            {alerts.length === 0 && <div className="alerts-dropdown__empty">Nothing to clean up.</div>}
            {alerts.map(a => (
              <div key={a.id} className="alerts-dropdown__item">
                <div className="alerts-dropdown__title">{a.title}</div>
                <div className="alerts-dropdown__detail">{a.detail}</div>
                <code className="alerts-dropdown__cmd">{a.command}</code>
                <button className="btn btn--secondary alerts-dropdown__copy" onClick={() => copy(a)}>
                  {copied === a.id ? <IconCheck size={13} /> : <IconCopy size={13} />}
                  <span>{copied === a.id ? 'Copied' : 'Copy cleanup command'}</span>
                </button>
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  )
}
