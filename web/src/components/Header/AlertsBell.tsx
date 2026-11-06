import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { IconBell, IconCheck, IconCopy } from '@tabler/icons-react'
import * as api from '../../api'
import { useStore } from '../../store'
import type { Alert, FlowApproval } from '../../types'
import '../Flows/Flows.css'

// Bell with what waits for the user: questions from flows, answered here, and the things
// lucode noticed and leaves to clean up (merged worktrees left behind), each with its command.
export function AlertsBell({ projectId }: { projectId: string }) {
  const [alerts, setAlerts] = useState<Alert[]>([])
  const [open, setOpen] = useState(false)
  const [copied, setCopied] = useState('')
  const [approvals, setApprovals] = useState<FlowApproval[]>([])
  const [decisionError, setDecisionError] = useState('')
  const flowVersion = useStore(s => s.flowVersion)
  const navigate = useNavigate()

  useEffect(() => {
    let live = true
    const load = () => api.listFlowApprovals(projectId).then(a => live && setApprovals(a)).catch(() => {})
    load()
    const id = setInterval(load, 60_000)
    return () => { live = false; clearInterval(id) }
  }, [projectId, flowVersion])

  async function decide(a: FlowApproval, decision: 'approved' | 'rejected') {
    try {
      await api.decideFlowApproval(a.seq, decision)
      setDecisionError('')
      setApprovals(prev => prev.filter(x => x.seq !== a.seq))
    } catch (e) {
      setDecisionError(e instanceof Error ? e.message : String(e))
      api.listFlowApprovals(projectId).then(setApprovals).catch(() => {})
    }
  }

  const count = alerts.length + approvals.length

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
        title={count ? `${count} alert(s)` : 'No alerts'}
        aria-label="Alerts"
        aria-expanded={open}
      >
        <IconBell size={16} />
        {count > 0 && <span className="header-icon-btn__badge">{count}</span>}
      </button>
      {open && (
        <>
          <div className="header-menu__backdrop" onClick={() => setOpen(false)} />
          <div className="nav-tree__dropdown header-menu__dropdown alerts-dropdown" role="dialog" aria-label="Alerts">
            {count === 0 && <div className="alerts-dropdown__empty">Nothing needs you.</div>}
            {decisionError && <div className="alerts-dropdown__detail" role="alert">{decisionError}</div>}
            {approvals.map(a => (
              <div key={a.seq} className="alerts-dropdown__item">
                <div className="alerts-dropdown__title">{a.question}</div>
                <div className="alerts-dropdown__detail">
                  {a.flow_name}{a.card_title ? ` · ${a.card_title}` : ''}{' · '}
                  <button
                    className="alerts-dropdown__link"
                    onClick={() => { setOpen(false); navigate(`/projects/${projectId}/flows?flow=${a.flow_id}&run=${a.run_id}`) }}
                  >See the run</button>
                </div>
                <div className="alerts-dropdown__decision">
                  <button className="btn btn--approve" onClick={() => decide(a, 'approved')}>Approve</button>
                  <button className="btn btn--deny" onClick={() => decide(a, 'rejected')}>Reject</button>
                </div>
              </div>
            ))}
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
