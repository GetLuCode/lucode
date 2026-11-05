import { useEffect, useState, useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  BarChart, Bar, LineChart, Line, AreaChart, Area,
  PieChart, Pie, Cell, Tooltip, XAxis, YAxis, CartesianGrid, ResponsiveContainer, Legend,
} from 'recharts'
import { useStore } from '../../store'
import * as api from '../../api'
import type { DailyUsageRow, ModelUsageRow, SessionUsageRow, UsageRange } from '../../api'
import { estimateCost, estimateUsageCost, formatCost } from '../../lib/tokenPricing'

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
}

function KPICard({ label, value }: KPICardProps) {
  return (
    <div className="usage-kpi">
      <span className="usage-kpi__value">{value}</span>
      <span className="usage-kpi__label">{label}</span>
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

interface ToolTotals {
  tool: string
  calls: number
  errors: number
  unmeasured: number
  resultTokens: number
  carriedRead: number
  outputTokens: number
  // dollars: asking for the tool, writing its result to the cache, re-reading it in later calls
  askCost: number
  writeCost: number
  readCost: number
}

// The tool rows come per model; add them up per tool, priced model by model.
function totalsByTool(rows: api.ToolCostRow[]): ToolTotals[] {
  const by = new Map<string, ToolTotals>()
  for (const r of rows) {
    const t = by.get(r.tool) ?? {
      tool: r.tool, calls: 0, errors: 0, unmeasured: 0, resultTokens: 0, carriedRead: 0, outputTokens: 0,
      askCost: 0, writeCost: 0, readCost: 0,
    }
    t.calls += r.calls
    t.errors += r.errors
    t.unmeasured += r.unmeasured
    t.resultTokens += r.result_tokens
    t.carriedRead += r.carried_read
    t.outputTokens += r.output_tokens
    t.askCost += estimateCost(r.model, 0, r.output_tokens)
    t.writeCost += estimateCost(r.model, r.uncached, 0, 0, r.written_5m, r.written_1h)
    t.readCost += estimateCost(r.model, 0, 0, r.carried_read)
    by.set(r.tool, t)
  }
  return [...by.values()]
}

type ToolSort = 'calls' | 'cost'

function ToolTable({ data }: { data: api.ToolCostRow[] }) {
  const [sortKey, setSortKey] = useState<ToolSort>('cost')
  const rows = useMemo(() => {
    const cost = (t: ToolTotals) => t.askCost + t.writeCost + t.readCost
    return totalsByTool(data).sort((a, b) => sortKey === 'cost' ? cost(b) - cost(a) : b.calls - a.calls)
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
            const cost = t.askCost + t.writeCost + t.readCost
            const tip = `Asking for it: ${formatCost(t.askCost)}\nWriting its results to the cache: ${formatCost(t.writeCost)}\n` +
              `Re-reading its results in later calls: ${formatCost(t.readCost)}` +
              (t.unmeasured ? `\n${t.unmeasured} call(s) ended a turn: their result could not be measured` : '')
            return (
              <tr key={t.tool}>
                <td className="usage-table__model">{t.tool.replace(/^mcp__lucode__/, 'lucode:')}</td>
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

// ─── Main page ───────────────────────────────────────────────────────────────

export function UsagePage() {
  const [range, setRange] = useState<UsageRange>('30d')
  const [chartType, setChartType] = useState<ChartType>('bar')
  const [showCache, setShowCache] = useState(false)

  const [dailyData, setDailyData] = useState<DailyUsageRow[]>([])
  const [modelData, setModelData] = useState<ModelUsageRow[]>([])
  const [sessionData, setSessionData] = useState<SessionUsageRow[]>([])
  const [toolData, setToolData] = useState<api.ToolCostRow[]>([])
  const [loading, setLoading] = useState(true)

  const loadProjects = useStore(s => s.loadProjects)
  useEffect(() => { loadProjects() }, [])

  useEffect(() => {
    setLoading(true)
    Promise.all([
      api.getUsageDaily(range),
      api.getUsageModels(range),
      api.getUsageSessions(range),
      api.getUsageTools(range).catch(() => [] as api.ToolCostRow[]), // an older hub has no such route
    ]).then(([d, m, s, t]) => {
      setDailyData(d)
      setModelData(m)
      setSessionData(s)
      setToolData(t)
      setLoading(false)
    }).catch(() => setLoading(false))
  }, [range])

  const kpis = useMemo(() => {
    const totalInput  = dailyData.reduce((s, r) => s + r.input_tokens, 0)
    const totalOutput = dailyData.reduce((s, r) => s + r.output_tokens, 0)
    const totalCacheR = dailyData.reduce((s, r) => s + r.cache_read_input_tokens, 0)
    const totalCacheW = dailyData.reduce((s, r) => s + r.cache_creation_input_tokens, 0)
    const totalTurns  = dailyData.reduce((s, r) => s + r.turn_count, 0)
    const totalCost   = modelData.reduce((s, r) =>
      s + estimateUsageCost(r.model, r), 0)
    return { totalInput, totalOutput, totalCacheR, totalCacheW, totalTurns, totalCost }
  }, [dailyData, modelData])

  return (
    <div className="usage-page">
      <div className="usage-page__header">
        <h1 className="usage-page__title">Usage</h1>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <RangeSelector value={range} onChange={setRange} />
        </div>
      </div>

      {loading ? (
        <div className="usage-page__loading">Loading…</div>
      ) : (
        <div className="usage-page__content">

          {/* KPI cards */}
          <div className="usage-kpis" style={{ marginBottom: 24 }}>
            <KPICard label="Input tokens" value={fmtNum(kpis.totalInput)} />
            <KPICard label="Output tokens" value={fmtNum(kpis.totalOutput)} />
            <KPICard label="Cache read" value={fmtNum(kpis.totalCacheR)} />
            <KPICard label="Cache write" value={fmtNum(kpis.totalCacheW)} />
            <KPICard label="Turns" value={fmtNum(kpis.totalTurns)} />
            <KPICard label="Est. cost" value={formatCost(kpis.totalCost)} />
          </div>

          {/* Daily usage chart */}
          <div className="usage-section">
            <div className="usage-section__header">
              <span className="usage-section-label">Daily token usage</span>
              <div style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
                <label className="usage-cache-toggle">
                  <input type="checkbox" checked={showCache} onChange={e => setShowCache(e.target.checked)} />
                  <span>Show cache</span>
                </label>
                <ChartTypeSelector value={chartType} onChange={setChartType} />
              </div>
            </div>
            <DailyChart data={dailyData} chartType={chartType} showCache={showCache} />
          </div>

          {/* Model distribution */}
          {modelData.length > 0 && (
            <div className="usage-section" style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 24 }}>
              <div>
                <div className="usage-section-label">Model distribution</div>
                <ModelPie data={modelData} />
              </div>
              <div>
                <div className="usage-section-label">Token usage by model</div>
                <table className="usage-table">
                  <thead>
                    <tr>
                      <th>Model</th>
                      <th>Input</th>
                      <th>Output</th>
                      <th>Cache R</th>
                      <th>Cache W</th>
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
          )}

          {/* Tools: most used and most expensive */}
          <div className="usage-section">
            <div className="usage-section-label">Tools</div>
            <ToolTable data={toolData} />
          </div>

          {/* Session breakdown */}
          <div className="usage-section">
            <div className="usage-section-label">Sessions ({sessionData.length})</div>
            <SessionTable data={sessionData} />
          </div>

          {/* Legacy per-account stats */}
          <div className="usage-section">
            <div className="usage-section-label">Account activity (stats-cache.json)</div>
            <p className="usage-page__subtitle" style={{ fontSize: 12, marginTop: 4 }}>
              Historical data from external stats-cache.json per account. Requires credfeeder to be running.
            </p>
          </div>
        </div>
      )}
    </div>
  )
}
