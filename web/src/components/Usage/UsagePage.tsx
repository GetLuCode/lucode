import { useEffect, useState } from 'react'
import { useStore } from '../../store'
import * as api from '../../api'
import type { AccountStats } from '../../api'
import type { Account } from '../../types'

// ─── helpers ─────────────────────────────────────────────────────────────────

function formatDate(dateStr: string): string {
  const d = new Date(dateStr + 'T00:00:00')
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

function fmtNum(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k'
  return String(n)
}

// ─── Mini bar chart ──────────────────────────────────────────────────────────

function ActivityChart({ data }: { data: api.DailyActivity[] }) {
  if (data.length === 0) return <div className="usage-chart__empty">No activity data</div>

  const recent = data.slice(-90)
  const maxMsg = Math.max(...recent.map(d => d.messageCount), 1)

  return (
    <div className="usage-chart">
      {recent.map(d => {
        const pct = (d.messageCount / maxMsg) * 100
        return (
          <div
            key={d.date}
            className="usage-chart__bar-wrap"
            title={`${formatDate(d.date)}: ${d.messageCount} msgs · ${d.sessionCount} sessions · ${d.toolCallCount} tools`}
          >
            <div
              className="usage-chart__bar"
              style={{ height: `${Math.max(2, pct)}%` }}
            />
          </div>
        )
      })}
    </div>
  )
}

// ─── Model usage table ───────────────────────────────────────────────────────

function ModelTable({ modelUsage }: { modelUsage: Record<string, api.ModelUsageEntry> }) {
  const entries = Object.entries(modelUsage).sort((a, b) =>
    (b[1].inputTokens + b[1].outputTokens) - (a[1].inputTokens + a[1].outputTokens)
  )
  if (entries.length === 0) return null

  return (
    <table className="usage-table">
      <thead>
        <tr>
          <th>Model</th>
          <th>Input</th>
          <th>Output</th>
          <th>Cache read</th>
          <th>Cache write</th>
        </tr>
      </thead>
      <tbody>
        {entries.map(([model, u]) => (
          <tr key={model}>
            <td className="usage-table__model">{model}</td>
            <td>{fmtNum(u.inputTokens)}</td>
            <td>{fmtNum(u.outputTokens)}</td>
            <td>{fmtNum(u.cacheReadTokens ?? 0)}</td>
            <td>{fmtNum(u.cacheWriteTokens ?? 0)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  )
}

// ─── Per-account stats panel ─────────────────────────────────────────────────

function AccountStatsPanel({ account }: { account: Account }) {
  const [stats, setStats] = useState<AccountStats | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    setLoading(true)
    setError(null)
    api.getAccountStats(account.id)
      .then(s => { setStats(s); setLoading(false) })
      .catch(e => { setError(e.message ?? 'Failed to load'); setLoading(false) })
  }, [account.id])

  const last7 = stats
    ? stats.dailyActivity.slice(-7).reduce(
        (acc, d) => ({ msgs: acc.msgs + d.messageCount, sessions: acc.sessions + d.sessionCount, tools: acc.tools + d.toolCallCount }),
        { msgs: 0, sessions: 0, tools: 0 }
      )
    : null

  return (
    <section className="usage-account">
      <div className="usage-account__header">
        <span className="usage-account__name">{account.name}</span>
        {stats?.firstSessionDate && (
          <span className="usage-account__since">since {formatDate(stats.firstSessionDate)}</span>
        )}
        {stats && (
          <span className="usage-account__computed">last computed {stats.lastComputedDate}</span>
        )}
      </div>

      {loading && <div className="usage-account__loading">Loading…</div>}
      {error && <div className="usage-account__error">{error}</div>}

      {stats && (
        <>
          <div className="usage-kpis">
            <div className="usage-kpi">
              <span className="usage-kpi__value">{fmtNum(stats.totalMessages)}</span>
              <span className="usage-kpi__label">total messages</span>
            </div>
            <div className="usage-kpi">
              <span className="usage-kpi__value">{fmtNum(stats.totalSessions)}</span>
              <span className="usage-kpi__label">total sessions</span>
            </div>
            {last7 && (
              <>
                <div className="usage-kpi">
                  <span className="usage-kpi__value">{fmtNum(last7.msgs)}</span>
                  <span className="usage-kpi__label">messages (7d)</span>
                </div>
                <div className="usage-kpi">
                  <span className="usage-kpi__value">{fmtNum(last7.sessions)}</span>
                  <span className="usage-kpi__label">sessions (7d)</span>
                </div>
                <div className="usage-kpi">
                  <span className="usage-kpi__value">{fmtNum(last7.tools)}</span>
                  <span className="usage-kpi__label">tool calls (7d)</span>
                </div>
              </>
            )}
          </div>

          <div className="usage-section-label">Daily messages — last 90 days</div>
          <ActivityChart data={stats.dailyActivity} />

          {stats.modelUsage && Object.keys(stats.modelUsage).length > 0 && (
            <>
              <div className="usage-section-label">Token usage by model</div>
              <ModelTable modelUsage={stats.modelUsage} />
            </>
          )}
        </>
      )}
    </section>
  )
}

// ─── Page ─────────────────────────────────────────────────────────────────────

export function UsagePage() {
  const accounts = useStore(s => s.accounts)
  const loadAccounts = useStore(s => s.loadAccounts)

  useEffect(() => {
    if (accounts.length === 0) loadAccounts()
  }, [])

  return (
    <div className="usage-page">
      <div className="usage-page__header">
        <h1 className="usage-page__title">Usage</h1>
        <p className="usage-page__subtitle">Historical activity from stats-cache.json for each account</p>
      </div>

      <div className="usage-page__content">
        {accounts.length === 0 ? (
          <div className="usage-page__empty">No accounts configured.</div>
        ) : (
          accounts.map(a => <AccountStatsPanel key={a.id} account={a} />)
        )}
      </div>
    </div>
  )
}
