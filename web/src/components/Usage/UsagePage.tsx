import { useEffect, useState, useMemo } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import {
  BarChart, Bar, LineChart, Line, AreaChart, Area,
  PieChart, Pie, Cell, Tooltip, XAxis, YAxis, CartesianGrid, ResponsiveContainer, Legend,
} from 'recharts'
import { useStore } from '../../store'
import * as api from '../../api'
import type { DailyUsageRow, ModelUsageRow, SessionUsageRow, UsageRange } from '../../api'
import { estimateCost, estimateUsageCost, formatCost } from '../../lib/tokenPricing'
import { totalsByTool, toolTotalCost, toolLabel } from '../../lib/toolCosts'
import type { ToolTotals } from '../../lib/toolCosts'

// ─── helpers ─────────────────────────────────────────────────────────────────

function fmtNum(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k'
  return String(n)
}

function fmtDate(dateStr: string): string {
  const d = new Date(dateStr + 'T00:00:00')
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

function fmtDuration(minutes: number): string {
  if (minutes < 60) return `${Math.round(minutes)}m`
  return `${(minutes / 60).toFixed(1)}h`
}

const MODEL_COLORS = ['#6366f1', '#d97706', '#22c55e', '#f59e0b', '#8b5cf6', '#3b82f6', '#ef4444', '#14b8a6']

// ─── Range selector ──────────────────────────────────────────────────────────

interface RangeSelectorProps {
  value: UsageRange
  onChange: (r: UsageRange) => void
}

function RangeSelector({ value, onChange }: RangeSelectorProps) {
  const options: { label: string; value: UsageRange }[] = [
    { label: '7d', value: '7d' },
    { label: '30d', value: '30d' },
    { label: '90d', value: '90d' },
    { label: 'All', value: 'all' },
  ]
  return (
    <div className="usage-range">
      {options.map(o => (
        <button
          key={o.value}
          className={`usage-range__btn${value === o.value ? ' usage-range__btn--active' : ''}`}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

// ─── Chart type selector ─────────────────────────────────────────────────────

type ChartType = 'bar' | 'line' | 'area'

interface ChartTypeSelectorProps {
  value: ChartType
  onChange: (t: ChartType) => void
}

function ChartTypeSelector({ value, onChange }: ChartTypeSelectorProps) {
  return (
    <div className="usage-range">
      {(['bar', 'line', 'area'] as ChartType[]).map(t => (
        <button
          key={t}
          className={`usage-range__btn${value === t ? ' usage-range__btn--active' : ''}`}
          onClick={() => onChange(t)}
        >
          {t}
        </button>
      ))}
    </div>
  )
}

// ─── Daily usage chart ───────────────────────────────────────────────────────

interface DailyChartProps {
  data: DailyUsageRow[]
  chartType: ChartType
  showCache: boolean
}

function aggregateByDate(data: DailyUsageRow[]): { date: string; input: number; output: number; cache_read: number; cache_write: number }[] {
  const map: Record<string, { input: number; output: number; cache_read: number; cache_write: number }> = {}
  for (const row of data) {
    if (!map[row.date]) map[row.date] = { input: 0, output: 0, cache_read: 0, cache_write: 0 }
    map[row.date].input      += row.input_tokens
    map[row.date].output     += row.output_tokens
    map[row.date].cache_read  += row.cache_read_input_tokens
    map[row.date].cache_write += row.cache_creation_input_tokens
  }
  return Object.entries(map)
    .map(([date, v]) => ({ date, ...v }))
    .sort((a, b) => a.date.localeCompare(b.date))
}

function DailyChart({ data, chartType, showCache }: DailyChartProps) {
  const aggregated = useMemo(() => aggregateByDate(data), [data])
  if (aggregated.length === 0) return <div className="usage-chart__empty">No data for this period</div>

  const chartData = aggregated.map(d => ({
    ...d,
    date: fmtDate(d.date),
  }))

  const commonProps = {
    data: chartData,
    margin: { top: 4, right: 8, left: 0, bottom: 0 },
  }

  const bars = [
    { key: 'input',       name: 'Input',       color: '#6366f1' },
    { key: 'output',      name: 'Output',      color: '#d97706' },
    ...(showCache ? [
      { key: 'cache_read',  name: 'Cache read',  color: '#22c55e' },
      { key: 'cache_write', name: 'Cache write', color: '#f59e0b' },
    ] : []),
  ]

  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const tooltipFormatter = (value: any) => fmtNum(Number(value))
  const yFormatter = (v: number) => fmtNum(v)

  return (
    <ResponsiveContainer width="100%" height={220}>
      {chartType === 'bar' ? (
        <BarChart {...commonProps}>
          <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
          <XAxis dataKey="date" tick={{ fontSize: 10, fill: 'var(--text-muted)' }} interval="preserveStartEnd" />
          <YAxis tickFormatter={yFormatter} tick={{ fontSize: 10, fill: 'var(--text-muted)' }} width={42} />
          <Tooltip formatter={tooltipFormatter} contentStyle={{ background: 'var(--bg-panel)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12 }} />
          <Legend wrapperStyle={{ fontSize: 11 }} />
          {bars.map(b => <Bar key={b.key} dataKey={b.key} name={b.name} fill={b.color} stackId="a" />)}
        </BarChart>
      ) : chartType === 'line' ? (
        <LineChart {...commonProps}>
          <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
          <XAxis dataKey="date" tick={{ fontSize: 10, fill: 'var(--text-muted)' }} interval="preserveStartEnd" />
          <YAxis tickFormatter={yFormatter} tick={{ fontSize: 10, fill: 'var(--text-muted)' }} width={42} />
          <Tooltip formatter={tooltipFormatter} contentStyle={{ background: 'var(--bg-panel)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12 }} />
          <Legend wrapperStyle={{ fontSize: 11 }} />
          {bars.map(b => <Line key={b.key} dataKey={b.key} name={b.name} stroke={b.color} dot={false} />)}
        </LineChart>
      ) : (
        <AreaChart {...commonProps}>
          <defs>
            {bars.map(b => (
              <linearGradient key={b.key} id={`grad-${b.key}`} x1="0" y1="0" x2="0" y2="1">
                <stop offset="5%" stopColor={b.color} stopOpacity={0.3} />
                <stop offset="95%" stopColor={b.color} stopOpacity={0} />
              </linearGradient>
            ))}
          </defs>
          <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
          <XAxis dataKey="date" tick={{ fontSize: 10, fill: 'var(--text-muted)' }} interval="preserveStartEnd" />
          <YAxis tickFormatter={yFormatter} tick={{ fontSize: 10, fill: 'var(--text-muted)' }} width={42} />
          <Tooltip formatter={tooltipFormatter} contentStyle={{ background: 'var(--bg-panel)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12 }} />
          <Legend wrapperStyle={{ fontSize: 11 }} />
          {bars.map(b => (
            <Area key={b.key} dataKey={b.key} name={b.name} stroke={b.color} fill={`url(#grad-${b.key})`} stackId="a" />
          ))}
        </AreaChart>
      )}
    </ResponsiveContainer>
  )
}

// ─── Model pie chart ─────────────────────────────────────────────────────────

function ModelPie({ data }: { data: ModelUsageRow[] }) {
  if (data.length === 0) return <div className="usage-chart__empty">No data</div>
  const pieData = data.map(r => ({
    name: r.model.replace(/claude-/i, '').replace(/-\d{8}$/, ''),
    value: r.input_tokens + r.output_tokens,
  }))
  return (
    <ResponsiveContainer width="100%" height={200}>
      <PieChart>
        <Pie data={pieData} dataKey="value" nameKey="name" cx="50%" cy="50%" outerRadius={80} label={({ name, percent }) => `${name} ${((percent ?? 0) * 100).toFixed(0)}%`} labelLine={false}>
          {pieData.map((_, i) => (
            <Cell key={i} fill={MODEL_COLORS[i % MODEL_COLORS.length]} />
          ))}
        </Pie>
        {/* eslint-disable-next-line @typescript-eslint/no-explicit-any */}
        <Tooltip formatter={(v: any) => fmtNum(Number(v))} contentStyle={{ background: 'var(--bg-panel)', border: '1px solid var(--border)', borderRadius: 6, fontSize: 12 }} />
      </PieChart>
    </ResponsiveContainer>
  )
}

// ─── KPI cards ───────────────────────────────────────────────────────────────

interface KPICardProps {
  label: string
  value: string
  sub?: string      // a second line under the label
  accent?: boolean  // the headline number
}

function KPICard({ label, value, sub, accent }: KPICardProps) {
  return (
    <div className={`usage-kpi${accent ? ' usage-kpi--accent' : ''}`}>
      <span className="usage-kpi__value">{value}</span>
      <span className="usage-kpi__label">{label}</span>
      {sub && <span className="usage-kpi__sub">{sub}</span>}
    </div>
  )
}

// ─── Session table ────────────────────────────────────────────────────────────

type SortKey = keyof SessionUsageRow

function SessionTable({ data }: { data: SessionUsageRow[] }) {
  const navigate = useNavigate()
  const projects = useStore(s => s.projects)
  const [sortKey, setSortKey] = useState<SortKey>('created_at')
  const [sortAsc, setSortAsc] = useState(false)

  const sorted = useMemo(() => {
    return [...data].sort((a, b) => {
      const av = a[sortKey], bv = b[sortKey]
      if (typeof av === 'number' && typeof bv === 'number') return sortAsc ? av - bv : bv - av
      return sortAsc
        ? String(av).localeCompare(String(bv))
        : String(bv).localeCompare(String(av))
    })
  }, [data, sortKey, sortAsc])

  const handleSort = (key: SortKey) => {
    if (sortKey === key) setSortAsc(v => !v)
    else { setSortKey(key); setSortAsc(false) }
  }

  const th = (key: SortKey, label: string) => (
    <th onClick={() => handleSort(key)} style={{ cursor: 'pointer', userSelect: 'none' }}>
      {label} {sortKey === key ? (sortAsc ? '↑' : '↓') : ''}
    </th>
  )

  if (data.length === 0) return <div className="usage-chart__empty">No sessions in this period</div>

  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="usage-table usage-table--sessions">
        <thead>
          <tr>
            {th('session_name', 'Session')}
            <th>Project</th>
            {th('model', 'Model')}
            {th('input_tokens', 'Input')}
            {th('output_tokens', 'Output')}
            {th('turn_count', 'Turns')}
            {th('duration_minutes', 'Duration')}
            <th>Est. cost</th>
            {th('created_at', 'Date')}
          </tr>
        </thead>
        <tbody>
          {sorted.map(row => {
            const proj = projects.find(p => p.id === row.project_id)
            const cost = estimateUsageCost(row.model, row)
            return (
              <tr
                key={row.session_id}
                onClick={() => navigate(`/projects/${row.project_id}/sessions/${row.session_id}`)}
                style={{ cursor: 'pointer' }}
                className="usage-table__row--clickable"
              >
                <td className="usage-table__model">{row.session_name || row.session_id.slice(0, 8)}</td>
                <td>{proj?.name ?? row.project_id.slice(0, 8)}</td>
                <td className="usage-table__model">{row.model.replace(/claude-/i, '').replace(/-\d{8}$/, '')}</td>
                <td>{fmtNum(row.input_tokens)}</td>
                <td>{fmtNum(row.output_tokens)}</td>
                <td>{row.turn_count}</td>
                <td>{fmtDuration(row.duration_minutes)}</td>
                <td>{formatCost(cost)}</td>
                <td>{fmtDate(row.created_at.slice(0, 10))}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

// ─── Tool table ───────────────────────────────────────────────────────────────

type ToolSort = 'calls' | 'cost'

function ToolTable({ data }: { data: api.ToolCostRow[] }) {
  const [sortKey, setSortKey] = useState<ToolSort>('cost')
  const rows = useMemo(() => {
    return totalsByTool(data).sort((a: ToolTotals, b: ToolTotals) =>
      sortKey === 'cost' ? toolTotalCost(b) - toolTotalCost(a) : b.calls - a.calls)
  }, [data, sortKey])

  if (rows.length === 0) {
    return (
      <div className="usage-chart__empty">
        No tool data in this period yet. It is collected from sessions run after the runners were updated.
      </div>
    )
  }
  const th = (key: ToolSort, label: string) => (
    <th onClick={() => setSortKey(key)} style={{ cursor: 'pointer', userSelect: 'none' }}>
      {label} {sortKey === key ? '↓' : ''}
    </th>
  )
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="usage-table">
        <thead>
          <tr>
            <th>Tool</th>
            {th('calls', 'Calls')}
            <th>Errors</th>
            <th title="Tokens its results added to the context, per call">Avg result</th>
            <th title="Result tokens x the later calls that re-read them from the cache">Re-read tokens</th>
            <th title="What the model wrote to ask for the tool">Output</th>
            {th('cost', 'Est. cost')}
          </tr>
        </thead>
        <tbody>
          {rows.map(t => {
            const measured = t.calls - t.unmeasured
            const cost = toolTotalCost(t)
            const tip = `Asking for it: ${formatCost(t.askCost)}\nWriting its results to the cache: ${formatCost(t.writeCost)}\n` +
              `Re-reading its results in later calls: ${formatCost(t.readCost)}` +
              (t.unmeasured ? `\n${t.unmeasured} call(s) ended a turn: their result could not be measured` : '')
            return (
              <tr key={t.tool}>
                <td className="usage-table__model">{toolLabel(t.tool)}</td>
                <td>{fmtNum(t.calls)}</td>
                <td>{t.errors || ''}</td>
                <td>{measured > 0 ? fmtNum(Math.round(t.resultTokens / measured)) : '—'}</td>
                <td>{fmtNum(t.carriedRead)}</td>
                <td>{fmtNum(t.outputTokens)}</td>
                <td title={tip}>{formatCost(cost)}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

// ─── Cost breakdown ───────────────────────────────────────────────────────────

const COST_PARTS = [
  { key: 'input', label: 'Input', color: '#3b82f6' },
  { key: 'output', label: 'Output', color: '#8b5cf6' },
  { key: 'cacheRead', label: 'Cache read', color: '#22c55e' },
  { key: 'write5m', label: 'Cache write (5 min)', color: '#f59e0b' },
  { key: 'write1h', label: 'Cache write (1 h)', color: '#d97706' },
] as const
type CostPart = typeof COST_PARTS[number]['key']

// Where the estimated cost goes: every kind of token priced apart, both cache TTLs included.
function costByPart(models: ModelUsageRow[]): Record<CostPart, number> {
  const t: Record<CostPart, number> = { input: 0, output: 0, cacheRead: 0, write5m: 0, write1h: 0 }
  for (const r of models) {
    const written = r.cache_creation_input_tokens
    const oneHour = Math.min(r.cache_creation_1h_input_tokens ?? 0, written)
    t.input += estimateCost(r.model, r.input_tokens, 0)
    t.output += estimateCost(r.model, 0, r.output_tokens)
    t.cacheRead += estimateCost(r.model, 0, 0, r.cache_read_input_tokens)
    t.write5m += estimateCost(r.model, 0, 0, 0, written - oneHour)
    t.write1h += estimateCost(r.model, 0, 0, 0, 0, oneHour)
  }
  return t
}

function CostBreakdown({ models }: { models: ModelUsageRow[] }) {
  const parts = useMemo(() => costByPart(models), [models])
  const total = COST_PARTS.reduce((s, p) => s + parts[p.key], 0)
  if (total <= 0) return <div className="usage-chart__empty">No cost in this period</div>
  return (
    <div className="usage-breakdown">
      <div className="usage-breakdown__bar" role="img" aria-label="Cost by kind of token">
        {COST_PARTS.map(p => parts[p.key] > 0 && (
          <span
            key={p.key}
            className="usage-breakdown__seg"
            style={{ width: `${(parts[p.key] / total) * 100}%`, background: p.color }}
            title={`${p.label}: ${formatCost(parts[p.key])}`}
          />
        ))}
      </div>
      <ul className="usage-breakdown__legend">
        {COST_PARTS.map(p => (
          <li key={p.key} className="usage-breakdown__row">
            <span className="usage-breakdown__dot" style={{ background: p.color }} />
            <span className="usage-breakdown__name">{p.label}</span>
            <span className="usage-breakdown__cost">{formatCost(parts[p.key])}</span>
            <span className="usage-breakdown__pct">{((parts[p.key] / total) * 100).toFixed(1)}%</span>
          </li>
        ))}
      </ul>
    </div>
  )
}

// ─── Project filter ───────────────────────────────────────────────────────────

function ProjectFilter({ value, onChange }: { value: string; onChange: (id: string) => void }) {
  const projects = useStore(s => s.projects)
  return (
    <label className="usage-filter">
      <span>Project</span>
      <select className="usage-select" value={value} onChange={e => onChange(e.target.value)}>
        <option value="">All projects</option>
        {projects.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
      </select>
    </label>
  )
}

// ─── Main page ───────────────────────────────────────────────────────────────

type UsageTab = 'overview' | 'tools' | 'sessions'

export function UsagePage() {
  const [params, setParams] = useSearchParams()
  const tabParam = params.get('tab')
  const tab: UsageTab = tabParam === 'tools' || tabParam === 'sessions' ? tabParam : 'overview'
  const setTab = (t: UsageTab) => setParams(t === 'overview' ? {} : { tab: t }, { replace: true })

  const [range, setRange] = useState<UsageRange>('30d')
  const [projectId, setProjectId] = useState('')
  const [chartType, setChartType] = useState<ChartType>('bar')
  const [showCache, setShowCache] = useState(false)

  const [dailyData, setDailyData] = useState<DailyUsageRow[]>([])
  const [modelData, setModelData] = useState<ModelUsageRow[]>([])
  const [sessionData, setSessionData] = useState<SessionUsageRow[]>([])
  const [toolData, setToolData] = useState<api.ToolCostRow[]>([])
  const [loadingOverview, setLoadingOverview] = useState(true)
  const [loadingDetail, setLoadingDetail] = useState(true)

  const loadProjects = useStore(s => s.loadProjects)
  useEffect(() => { loadProjects() }, [])

  // Overview: the whole account, by day and by model
  useEffect(() => {
    setLoadingOverview(true)
    Promise.all([api.getUsageDaily(range), api.getUsageModels(range)])
      .then(([d, m]) => { setDailyData(d); setModelData(m) })
      .catch(() => {})
      .finally(() => setLoadingOverview(false))
  }, [range])

  // Sessions and tools: they can be narrowed to one project
  useEffect(() => {
    setLoadingDetail(true)
    Promise.all([
      api.getUsageSessions(range, projectId || undefined),
      api.getUsageTools(range, projectId || undefined).catch(() => [] as api.ToolCostRow[]), // an older hub has no such route
    ])
      .then(([s, t]) => { setSessionData(s); setToolData(t) })
      .catch(() => {})
      .finally(() => setLoadingDetail(false))
  }, [range, projectId])

  const kpis = useMemo(() => {
    const sum = (f: (r: DailyUsageRow) => number) => dailyData.reduce((s, r) => s + f(r), 0)
    return {
      input: sum(r => r.input_tokens),
      output: sum(r => r.output_tokens),
      cacheRead: sum(r => r.cache_read_input_tokens),
      cacheWrite: sum(r => r.cache_creation_input_tokens),
      cacheWrite1h: sum(r => r.cache_creation_1h_input_tokens ?? 0),
      turns: sum(r => r.turn_count),
      cost: modelData.reduce((s, r) => s + estimateUsageCost(r.model, r), 0),
    }
  }, [dailyData, modelData])

  const tabs: { id: UsageTab; label: string; count?: number }[] = [
    { id: 'overview', label: 'Overview' },
    { id: 'tools', label: 'Tools', count: toolData.length ? new Set(toolData.map(t => t.tool)).size : undefined },
    { id: 'sessions', label: 'Sessions', count: sessionData.length || undefined },
  ]

  return (
    <div className="usage-page">
      <div className="usage-page__header">
        <div>
          <h1 className="usage-page__title">Usage</h1>
          <p className="usage-page__subtitle">Tokens and estimated cost (list prices, not billed)</p>
        </div>
        <RangeSelector value={range} onChange={setRange} />
      </div>

      <nav className="usage-tabs" aria-label="Usage sections">
        {tabs.map(t => (
          <button
            key={t.id}
            className={`usage-tab${tab === t.id ? ' usage-tab--active' : ''}`}
            onClick={() => setTab(t.id)}
            aria-current={tab === t.id ? 'page' : undefined}
          >
            {t.label}
            {t.count !== undefined && <span className="usage-tab__badge">{t.count}</span>}
          </button>
        ))}
      </nav>

      <div className="usage-page__content">
        {tab === 'overview' && (loadingOverview ? (
          <div className="usage-page__loading">Loading…</div>
        ) : (
          <>
            <div className="usage-kpis">
              <KPICard label="Est. cost" value={formatCost(kpis.cost)} accent />
              <KPICard label="Turns" value={fmtNum(kpis.turns)} />
              <KPICard label="Input" value={fmtNum(kpis.input)} />
              <KPICard label="Output" value={fmtNum(kpis.output)} />
              <KPICard label="Cache read" value={fmtNum(kpis.cacheRead)} />
              <KPICard
                label="Cache write"
                value={fmtNum(kpis.cacheWrite)}
                sub={`1 h ${fmtNum(kpis.cacheWrite1h)} · 5 min ${fmtNum(Math.max(0, kpis.cacheWrite - kpis.cacheWrite1h))}`}
              />
            </div>

            <section className="usage-section">
              <div className="usage-section-label">Where the cost goes</div>
              <CostBreakdown models={modelData} />
            </section>

            <section className="usage-section">
              <div className="usage-section__header">
                <span className="usage-section-label">Daily token usage</span>
                <div className="usage-section__tools">
                  <label className="usage-cache-toggle">
                    <input type="checkbox" checked={showCache} onChange={e => setShowCache(e.target.checked)} />
                    <span>Show cache</span>
                  </label>
                  <ChartTypeSelector value={chartType} onChange={setChartType} />
                </div>
              </div>
              <DailyChart data={dailyData} chartType={chartType} showCache={showCache} />
            </section>

            {modelData.length > 0 && (
              <section className="usage-section">
                <div className="usage-section-label">By model</div>
                <div className="usage-models">
                  <ModelPie data={modelData} />
                  <div style={{ overflowX: 'auto' }}>
                    <table className="usage-table">
                      <thead>
                        <tr>
                          <th>Model</th>
                          <th>Input</th>
                          <th>Output</th>
                          <th>Cache R</th>
                          <th title="Cache writes: 1 hour cache / 5 minute cache">Cache W</th>
                          <th>Turns</th>
                          <th>Cost</th>
                        </tr>
                      </thead>
                      <tbody>
                        {modelData.map(r => (
                          <tr key={r.model}>
                            <td className="usage-table__model">{r.model.replace(/claude-/i, '').replace(/-\d{8}$/, '')}</td>
                            <td>{fmtNum(r.input_tokens)}</td>
                            <td>{fmtNum(r.output_tokens)}</td>
                            <td>{fmtNum(r.cache_read_input_tokens)}</td>
                            <td>{fmtNum(r.cache_creation_input_tokens)}</td>
                            <td>{r.turn_count}</td>
                            <td>{formatCost(estimateUsageCost(r.model, r))}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </div>
              </section>
            )}

            <p className="usage-page__footnote">
              Cache writes are priced by their TTL: 1 hour at twice the input price, 5 minutes at 1.25 times.
              Turns recorded before the runners reported the split count as 5 minutes.
            </p>
          </>
        ))}

        {tab === 'tools' && (
          <section className="usage-section">
            <div className="usage-toolbar">
              <div className="usage-section-label">Most used and most expensive tools</div>
              <ProjectFilter value={projectId} onChange={setProjectId} />
            </div>
            {loadingDetail ? <div className="usage-page__loading">Loading…</div> : <ToolTable data={toolData} />}
            <p className="usage-page__footnote">
              A tool&apos;s cost is what the model wrote to ask for it, plus its result: written to the cache once and
              re-read by every later call. Results of tools called in parallel are split by size; sessions from before
              the runners reported result sizes split them equally.
            </p>
          </section>
        )}

        {tab === 'sessions' && (
          <section className="usage-section">
            <div className="usage-toolbar">
              <div className="usage-section-label">Sessions ({sessionData.length})</div>
              <ProjectFilter value={projectId} onChange={setProjectId} />
            </div>
            {loadingDetail ? <div className="usage-page__loading">Loading…</div> : <SessionTable data={sessionData} />}
          </section>
        )}
      </div>
    </div>
  )
}
