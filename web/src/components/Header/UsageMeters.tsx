import { useEffect, useState } from 'react'
import * as api from '../../api'
import type { AIProfile } from '../../types'

interface Meter { key: string; label: string; ai: AIProfile; five: number | null; seven: number | null; fiveReset: string | null; sevenReset: string | null; error?: string }

// Usage of every preconfigured AI (account) of every runner: 5-hour and weekly plan
// windows, read from Anthropic's /api/oauth/usage through the runner that owns the dir.
export function UsageMeters() {
  const [meters, setMeters] = useState<Meter[]>([])

  useEffect(() => {
    let live = true
    async function load() {
      try {
        const runners = await api.listRunners()
        const out: Meter[] = []
        await Promise.all(runners.map(async r => {
          const ais = await api.listAIProfiles(r.id).catch(() => [] as AIProfile[])
          await Promise.all(ais.map(async ai => {
            const m: Meter = { key: `${r.id}/${ai.id}`, label: ai.email || ai.name, ai, five: null, seven: null, fiveReset: null, sevenReset: null }
            try {
              const u = await api.getAIProfileUsage(r.id, ai.id)
              m.five = u.five_hour?.utilization ?? null
              m.seven = u.seven_day?.utilization ?? null
              m.fiveReset = u.five_hour?.resets_at ?? null
              m.sevenReset = u.seven_day?.resets_at ?? null
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

  if (!meters.length) return null
  return (
    <div className="usage-meters">
      {meters.map(m => (
        <div key={m.key} className="usage-meter" title={tooltip(m)}>
          <span className="usage-meter__name">{m.ai.name}</span>
          {m.error ? <span className="usage-meter__err">n/a</span> : (
            <>
              <Bar label="5h" pct={m.five} />
              <Bar label="7d" pct={m.seven} />
            </>
          )}
        </div>
      ))}
    </div>
  )
}

function Bar({ label, pct }: { label: string; pct: number | null }) {
  const v = pct == null ? 0 : Math.max(0, Math.min(100, pct))
  const level = v >= 90 ? 'high' : v >= 70 ? 'mid' : 'low'
  return (
    <span className="usage-bar">
      <span className="usage-bar__label">{label}</span>
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
