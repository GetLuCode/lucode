import { useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import * as api from '../../api'
import type { AIProfile } from '../../types'
import { useIsMobile } from '../../hooks/useIsMobile'
import { AccountAvatar } from './AccountAvatar'

interface Win { pct: number | null; reset: string | null }
interface Meter {
  key: string
  runnerId: string
  label: string
  ai: AIProfile
  five: number | null
  seven: number | null
  fiveReset: string | null
  sevenReset: string | null
  extra: { label: string; win: Win }[]
  error?: string
}

// Usage of every preconfigured AI (account) of every runner: 5-hour and weekly plan
// windows, read from Anthropic's /api/oauth/usage through the runner that owns the dir.
export function UsageMeters() {
  const [meters, setMeters] = useState<Meter[]>([])
  const [open, setOpen] = useState(false)
  const isMobile = useIsMobile() // phone: just the avatar, the bars are in the modal

  useEffect(() => {
    let live = true
    async function load() {
      try {
        const runners = await api.listRunners()
        const out: Meter[] = []
        await Promise.all(runners.map(async r => {
          const ais = await api.listAIProfiles(r.id).catch(() => [] as AIProfile[])
          await Promise.all(ais.map(async ai => {
            const m: Meter = { key: `${r.id}/${ai.id}`, runnerId: r.id, label: ai.email || ai.name, ai, five: null, seven: null, fiveReset: null, sevenReset: null, extra: [] }
            try {
              const u = await api.getAIProfileUsage(r.id, ai.id)
              m.five = u.five_hour?.utilization ?? null
              m.seven = u.seven_day?.utilization ?? null
              m.fiveReset = u.five_hour?.resets_at ?? null
              m.sevenReset = u.seven_day?.resets_at ?? null
              if (u.seven_day_opus) m.extra.push({ label: '7d Opus', win: { pct: u.seven_day_opus.utilization ?? null, reset: u.seven_day_opus.resets_at ?? null } })
              if (u.seven_day_sonnet) m.extra.push({ label: '7d Sonnet', win: { pct: u.seven_day_sonnet.utilization ?? null, reset: u.seven_day_sonnet.resets_at ?? null } })
            } catch (e) {
              m.error = e instanceof Error ? e.message : String(e)
            }
            out.push(m)
          }))
        }))
        out.sort((a, b) => a.key.localeCompare(b.key))
        if (live) setMeters(out)
      } catch { /* hub unreachable: keep what is shown */ }
    }
    load()
    const id = setInterval(load, 120_000)
    return () => { live = false; clearInterval(id) }
  }, [])

  useEffect(() => {
    if (!open) return
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') setOpen(false) }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])

  // The account in use (set from the modal); without one, the first account stands in.
  const [active, setActive] = useState<api.ActiveAI | null>(null)
  useEffect(() => {
    if (open) return // reloaded when the modal closes, since that is where it changes
    api.getActiveAI().then(setActive).catch(() => setActive(null))
  }, [open])

  if (!meters.length) return null
  const inUse = meters.find(m => active && active.ai_profile_id === m.ai.id && active.runner_id === m.runnerId) ?? meters[0]
  const shown = [inUse]
  return (
    <>
      <div
        className="usage-meters usage-meters--clickable"
        role="button"
        tabIndex={0}
        title="Click for full usage details"
        onClick={() => setOpen(true)}
        onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setOpen(true) } }}
      >
        {shown.map(m => (
          <div key={m.key} className="usage-meter" title={tooltip(m)}>
            <AccountAvatar id={m.ai.id} name={m.ai.name} size={20} />
            {isMobile ? null : m.error ? <span className="usage-meter__err">n/a</span> : (
              <>
                <Bar label="5h" pct={m.five} />
                <Bar label="7d" pct={m.seven} />
              </>
            )}
          </div>
        ))}
      </div>
      {open && createPortal(<UsageModal meters={meters} onClose={() => setOpen(false)} />, document.body)}
    </>
  )
}

function UsageModal({ meters, onClose }: { meters: Meter[]; onClose: () => void }) {
  const [active, setActive] = useState<api.ActiveAI | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => { api.getActiveAI().then(setActive).catch(() => setActive({ runner_id: '', ai_profile_id: '' })) }, [])
  const isActive = (m: Meter) => !!active && active.ai_profile_id === m.ai.id && active.runner_id === m.runnerId
  const pick = async (m: Meter) => {
    setErr('')
    const next = isActive(m) ? { runner_id: '', ai_profile_id: '' } : { runner_id: m.runnerId, ai_profile_id: m.ai.id }
    try { setActive(await api.setActiveAI(next)) } catch (e) { setErr(e instanceof Error ? e.message : String(e)) }
  }
  return (
    <div className="usage-modal-overlay" onClick={onClose}>
      <div className="usage-modal" role="dialog" aria-label="Account usage" onClick={e => e.stopPropagation()}>
        <div className="usage-modal__header">
          <span className="usage-modal__title">Account usage</span>
          <button className="usage-modal__close" onClick={onClose} aria-label="Close">×</button>
        </div>
        <div className="usage-modal__body">
          <p className="usage-modal__hint">
            Select the account that the orchestrator and the agents set to “Project&apos;s AI” or “default” will use
            (only on the runner that owns it).
          </p>
          {err && <div className="usage-modal__error">{err}</div>}
          {meters.map(m => (
            <div key={m.key} className={`usage-modal__account${isActive(m) ? ' usage-modal__account--active' : ''}`}>
              <div className="usage-modal__account-head">
                <AccountAvatar id={m.ai.id} name={m.ai.name} size={24} />
                <span className="usage-modal__account-name">{m.ai.name}</span>
                {m.ai.plan && <span className="usage-modal__badge">{m.ai.plan}</span>}
                <button
                  className={`usage-modal__use${isActive(m) ? ' usage-modal__use--on' : ''}`}
                  onClick={() => pick(m)}
                  disabled={!active}
                >
                  {isActive(m) ? '✓ In use' : 'Use'}
                </button>
              </div>
              <div className="usage-modal__meta">{m.ai.email || '–'} · runner {m.runnerId}</div>
              {m.error ? (
                <div className="usage-modal__error">{m.error}</div>
              ) : (
                <>
                  <Row label="5-hour" win={{ pct: m.five, reset: m.fiveReset }} />
                  <Row label="7-day" win={{ pct: m.seven, reset: m.sevenReset }} />
                  {m.extra.map(x => <Row key={x.label} label={x.label} win={x.win} />)}
                </>
              )}
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}

function Row({ label, win }: { label: string; win: Win }) {
  return (
    <div className="usage-modal__row">
      <span className="usage-modal__row-label">{label}</span>
      <Bar pct={win.pct} wide />
      <span className="usage-modal__row-reset">{win.reset ? `resets ${new Date(win.reset).toLocaleString()} (${until(win.reset)})` : '–'}</span>
    </div>
  )
}

function until(iso: string): string {
  const ms = new Date(iso).getTime() - Date.now()
  if (!isFinite(ms) || ms <= 0) return 'now'
  const mins = Math.round(ms / 60000)
  if (mins < 60) return `in ${mins}m`
  const h = Math.floor(mins / 60)
  if (h < 24) return `in ${h}h ${mins % 60}m`
  return `in ${Math.floor(h / 24)}d ${h % 24}h`
}

function Bar({ label, pct, wide }: { label?: string; pct: number | null; wide?: boolean }) {
  const v = pct == null ? 0 : Math.max(0, Math.min(100, pct))
  const level = v >= 90 ? 'high' : v >= 70 ? 'mid' : 'low'
  return (
    <span className={`usage-bar${wide ? ' usage-bar--wide' : ''}`}>
      {label && <span className="usage-bar__label">{label}</span>}
      <span className="usage-bar__track"><span className={`usage-bar__fill usage-bar__fill--${level}`} style={{ width: `${v}%` }} /></span>
      <span className="usage-bar__pct">{pct == null ? '–' : `${Math.round(v)}%`}</span>
    </span>
  )
}

function tooltip(m: Meter): string {
  const head = m.label + (m.ai.plan ? ` (${m.ai.plan})` : '')
  if (m.error) return `${head}\n${m.error}`
  const fmt = (s: string | null) => (s ? new Date(s).toLocaleString() : '–')
  return `${head}\n5h: ${m.five ?? '–'}% · resets ${fmt(m.fiveReset)}\n7d: ${m.seven ?? '–'}% · resets ${fmt(m.sevenReset)}`
}
