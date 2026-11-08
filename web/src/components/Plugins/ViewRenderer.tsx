import { createContext, useCallback, useContext, useMemo, useRef, useState, type CSSProperties } from 'react'
import { Bar, BarChart, CartesianGrid, Legend, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'
import { IconAlertTriangle, IconExternalLink, IconInbox, IconLoader2, IconTable, IconChartBar } from '@tabler/icons-react'
import * as api from '../../api'
import { conversationElementModel, conversationElementSource, pluginErrorMessage } from '../../lib/plugins'
import { classifyRpcResult, formArgs, formDefaults, type ViewAction, type ViewField, type ViewNode } from '../../lib/pluginView'
import { pluginMarkdownToHtml } from '../../lib/safeMarkdown'
import { sanitizeHtml } from '../../lib/sanitizeHtml'
import { usePlugins } from '../../store/plugins'
import { OrchestratorPanel } from '../Orchestrator/OrchestratorPanel'
import { PluginDialog } from './PluginDialog'
import { PluginIcon, PluginOrigin } from './PluginIcon'
import './Plugins.css'

// Draws a plugin's view tree with host components. The tree has been through sanitizeViewTree:
// types it does not know never arrive, and no value of it is ever inserted as markup, except
// markdown, which pluginMarkdownToHtml and sanitizeHtml have both been over.

interface ViewContextValue {
  pluginId: string
  projectId: string | null
  run: (action: ViewAction, extra?: Record<string, unknown>) => Promise<void>
}
const ViewContext = createContext<ViewContextValue | null>(null)
const useView = () => useContext(ViewContext)!

export function ViewRenderer({ pluginId, projectId, tree, onTree }: {
  pluginId: string
  projectId: string | null
  tree: ViewNode
  onTree: (tree: ViewNode) => void
}) {
  const [asking, setAsking] = useState<{ question: string; answer: (yes: boolean) => void } | null>(null)
  const onTreeRef = useRef(onTree)
  onTreeRef.current = onTree

  const run = useCallback(async (action: ViewAction, extra?: Record<string, unknown>) => {
    if (action.confirm) {
      const question = action.confirm
      const yes = await new Promise<boolean>(answer => setAsking({ question, answer }))
      setAsking(null)
      if (!yes) return
    }
    const outcome = classifyRpcResult(await api.callPluginRpc(pluginId, action.rpc, projectId, extra ?? action.args))
    if (outcome.kind === 'tree') onTreeRef.current(outcome.tree)
    else if (outcome.kind === 'toast') usePlugins.getState().toast(outcome.text, outcome.tone, pluginId)
  }, [pluginId, projectId])

  const value = useMemo(() => ({ pluginId, projectId, run }), [pluginId, projectId, run])
  return (
    <ViewContext.Provider value={value}>
      <Node node={tree} />
      {asking && (
        <PluginDialog
          title="Please confirm"
          subtitle={<PluginOrigin pluginId={pluginId} />}
          onClose={() => asking.answer(false)}
          footer={<>
            <button className="pv-btn pv-btn--ghost" onClick={() => asking.answer(false)}>Cancel</button>
            <button className="pv-btn pv-btn--primary" onClick={() => asking.answer(true)}>Continue</button>
          </>}
        >
          <p className="plugin-dialog__text">{asking.question}</p>
        </PluginDialog>
      )}
    </ViewContext.Provider>
  )
}

function Nodes({ nodes }: { nodes: ViewNode[] }) {
  return <>{nodes.map((n, i) => <Node key={i} node={n} />)}</>
}

function Node({ node }: { node: ViewNode }) {
  switch (node.type) {
    case 'stack':
      return <div className={`pv-stack pv-stack--${node.direction}`} style={{ gap: node.gap }}><Nodes nodes={node.children} /></div>
    case 'grid':
      return <div className={`pv-grid${node.columns === 2 ? ' pv-grid--two' : ''}`} style={{ '--pv-columns': node.columns } as CSSProperties}><Nodes nodes={node.children} /></div>
    case 'card':
      return <ViewCard node={node} />
    case 'heading': {
      const Tag = `h${Math.min(node.level + 1, 5)}` as 'h2' // the page's own title is the h1
      return <Tag className={`pv-heading pv-heading--${node.level}`}>{node.text}</Tag>
    }
    case 'text':
      return <p className={`pv-text pv-tone--${node.tone}`}>{node.text}</p>
    case 'markdown':
      return <ViewMarkdown text={node.text} />
    case 'stat':
      return (
        <div className="pv-stat">
          <span className="pv-stat__label">{node.label}</span>
          <span className="pv-stat__value">{node.value}</span>
          {node.delta && <span className={`pv-stat__delta pv-tone--${node.tone === 'default' ? 'muted' : node.tone}`}>{node.delta}</span>}
        </div>
      )
    case 'badge':
      return <span className={`pv-badge pv-badge--${node.tone}`}>{node.text}</span>
    case 'table':
      return <ViewTable columns={node.columns} rows={node.rows} truncated={node.truncated} />
    case 'progress':
      return (
        <div className="pv-progress">
          {node.label && (
            <div className="pv-progress__head">
              <span>{node.label}</span>
              <span className="pv-progress__value">{Math.round(node.percent)}%</span>
            </div>
          )}
          <div className="pv-progress__track" role="progressbar" aria-valuenow={Math.round(node.percent)} aria-valuemin={0} aria-valuemax={100} aria-label={node.label || 'Progress'}>
            <div className={`pv-progress__fill pv-fill--${node.tone}`} style={{ transform: `scaleX(${node.percent / 100})` }} />
          </div>
        </div>
      )
    case 'list':
      return <ViewList node={node} />
    case 'chart':
      return <ViewChart node={node} />
    case 'button':
      return <ViewButton node={node} />
    case 'form':
      return <ViewForm node={node} />
    case 'link':
      return (
        <a className="pv-link" href={node.href} target="_blank" rel="noreferrer noopener">
          {node.text}<IconExternalLink size={13} aria-hidden />
        </a>
      )
    case 'divider':
      return <hr className="pv-divider" />
    case 'empty':
      return <ViewEmpty title={node.title} text={node.text} icon={node.icon} />
    case 'conversation':
      return <ViewConversation conversationId={node.conversationId} model={node.model} />
    default:
      return null
  }
}

function ViewCard({ node }: { node: Extract<ViewNode, { type: 'card' }> }) {
  const { pluginId } = useView()
  const hasHead = node.title || node.subtitle || node.icon
  return (
    <section className="pv-card">
      {hasHead && (
        <header className="pv-card__head">
          {node.icon && <PluginIcon pluginId={pluginId} path={node.icon} size={18} fallback={<></>} />}
          <div className="pv-card__titles">
            {node.title && <h3 className="pv-card__title">{node.title}</h3>}
            {node.subtitle && <p className="pv-card__subtitle">{node.subtitle}</p>}
          </div>
        </header>
      )}
      {node.children.length > 0 && <div className="pv-card__body"><Nodes nodes={node.children} /></div>}
    </section>
  )
}

function ViewMarkdown({ text }: { text: string }) {
  const html = useMemo(() => sanitizeHtml(pluginMarkdownToHtml(text)), [text])
  return <div className="pv-markdown msg__markdown" dangerouslySetInnerHTML={{ __html: html }} />
}

function ViewTable({ columns, rows, truncated }: { columns: { key: string; label: string; align: string }[]; rows: Record<string, string | number>[]; truncated?: number }) {
  return (
    <div className="pv-table__scroll">
      <table className="pv-table">
        <thead>
          <tr>{columns.map(c => <th key={c.key} style={{ textAlign: c.align as CSSProperties['textAlign'] }}>{c.label}</th>)}</tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i}>{columns.map(c => <td key={c.key} style={{ textAlign: c.align as CSSProperties['textAlign'] }}>{r[c.key]}</td>)}</tr>
          ))}
          {rows.length === 0 && <tr><td className="pv-table__none" colSpan={Math.max(columns.length, 1)}>Nothing here yet.</td></tr>}
        </tbody>
      </table>
      {!!truncated && <p className="pv-table__more">{truncated} more rows not shown.</p>}
    </div>
  )
}

function ViewList({ node }: { node: Extract<ViewNode, { type: 'list' }> }) {
  const { run } = useView()
  const [busy, setBusy] = useState(-1)
  const [error, setError] = useState('')
  const act = async (i: number, action: ViewAction) => {
    setBusy(i)
    setError('')
    try { await run(action) } catch (e) { setError(pluginErrorMessage(e)) } finally { setBusy(-1) }
  }
  return (
    <>
      <ul className="pv-list">
        {node.items.map((it, i) => {
          if (it.node) return <li key={i} className="pv-list__item"><Node node={it.node} /></li>
          const body = (
            <>
              <span className="pv-list__text">
                <span className="pv-list__title">{it.title}</span>
                {it.subtitle && <span className="pv-list__subtitle">{it.subtitle}</span>}
              </span>
              {it.badge && <span className="pv-badge pv-badge--default">{it.badge}</span>}
              {busy === i && <IconLoader2 size={14} className="plugin-spin" aria-label="Working" />}
              {it.href && <IconExternalLink size={13} className="pv-list__go" aria-hidden />}
            </>
          )
          return (
            <li key={i} className="pv-list__item">
              {it.href
                ? <a className="pv-list__row pv-list__row--on" href={it.href} target="_blank" rel="noreferrer noopener">{body}</a>
                : it.action
                  ? <button className="pv-list__row pv-list__row--on" disabled={busy >= 0} onClick={() => act(i, it.action!)}>{body}</button>
                  : <div className="pv-list__row">{body}</div>}
            </li>
          )
        })}
      </ul>
      {error && <ViewError text={error} />}
    </>
  )
}

// The categorical slots, checked for dark surfaces with the palette validator: identity only,
// always with a legend, in this fixed order so a series keeps its color across renders.
const SERIES_COLORS = ['#00ab8c', '#5f8ee8', '#c08433', '#a866c8', '#d0555f']

function ViewChart({ node }: { node: Extract<ViewNode, { type: 'chart' }> }) {
  const [asTable, setAsTable] = useState(false)
  const axis = { fontSize: 11, fill: 'var(--text-muted)' }
  const tooltip = { background: 'var(--bg-panel)', border: '1px solid var(--border-bright)', borderRadius: 8, fontSize: 12, boxShadow: 'var(--shadow)' }
  const several = node.series.length >= 2
  const allowDecimals = node.rows.some(r => node.series.some(s => typeof r[s.key] === 'number' && !Number.isInteger(r[s.key])))
  const legendText = (value: string) => <span style={{ color: 'var(--text-dim)' }}>{value}</span>
  const columns = [{ key: 'x', label: '', align: 'left' }, ...node.series.map(s => ({ key: s.key, label: s.name, align: 'right' }))]
  const body = node.kind === 'line' ? (
    <LineChart data={node.rows} margin={{ top: 8, right: 12, bottom: 0, left: 0 }}>
      <CartesianGrid vertical={false} stroke="var(--border)" />
      <XAxis dataKey="x" tick={axis} tickLine={false} axisLine={{ stroke: 'var(--border-bright)' }} minTickGap={24} />
      <YAxis tick={axis} tickLine={false} axisLine={false} width={44} allowDecimals={allowDecimals} />
      <Tooltip contentStyle={tooltip} cursor={{ stroke: 'var(--border-bright)' }} />
      {several && <Legend wrapperStyle={{ fontSize: 12 }} iconType="circle" iconSize={8} formatter={legendText} />}
      {node.series.map((s, i) => (
        <Line key={s.key} dataKey={s.key} name={s.name} stroke={SERIES_COLORS[i]} strokeWidth={2} dot={false} activeDot={{ r: 4, stroke: 'var(--bg-panel)', strokeWidth: 2 }} isAnimationActive={false} />
      ))}
    </LineChart>
  ) : (
    <BarChart data={node.rows} margin={{ top: 8, right: 12, bottom: 0, left: 0 }} barGap={2} barCategoryGap="28%">
      <CartesianGrid vertical={false} stroke="var(--border)" />
      <XAxis dataKey="x" tick={axis} tickLine={false} axisLine={{ stroke: 'var(--border-bright)' }} minTickGap={16} />
      <YAxis tick={axis} tickLine={false} axisLine={false} width={44} allowDecimals={allowDecimals} />
      <Tooltip contentStyle={tooltip} cursor={{ fill: 'var(--bg-hover)' }} />
      {several && <Legend wrapperStyle={{ fontSize: 12 }} iconType="circle" iconSize={8} formatter={legendText} />}
      {node.series.map((s, i) => (
        <Bar key={s.key} dataKey={s.key} name={s.name} fill={SERIES_COLORS[i]} radius={[4, 4, 0, 0]} maxBarSize={36} isAnimationActive={false} />
      ))}
    </BarChart>
  )
  return (
    <figure className="pv-chart">
      <figcaption className={`pv-chart__head${node.title ? '' : ' pv-chart__head--bare'}`}>
        {node.title && <span>{node.title}</span>}
        <button className="plugin-icon-btn" onClick={() => setAsTable(v => !v)} title={asTable ? 'Show as a chart' : 'Show as a table'} aria-label={asTable ? 'Show as a chart' : 'Show as a table'}>
          {asTable ? <IconChartBar size={15} /> : <IconTable size={15} />}
        </button>
      </figcaption>
      {node.rows.length === 0
        ? <p className="pv-table__more">No data yet.</p>
        : asTable
          ? <ViewTable columns={columns} rows={node.rows} />
          : <div className="pv-chart__plot"><ResponsiveContainer width="100%" height="100%">{body}</ResponsiveContainer></div>}
      {node.hiddenSeries > 0 && <p className="pv-table__more">{node.hiddenSeries} more series not shown.</p>}
    </figure>
  )
}

function ViewError({ text }: { text: string }) {
  return <p className="pv-error" role="alert"><IconAlertTriangle size={14} aria-hidden /><span>{text}</span></p>
}

function ViewButton({ node }: { node: Extract<ViewNode, { type: 'button' }> }) {
  const { pluginId, run } = useView()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const click = async () => {
    if (!node.action) return
    setBusy(true)
    setError('')
    try { await run(node.action) } catch (e) { setError(pluginErrorMessage(e)) } finally { setBusy(false) }
  }
  return (
    <span className="pv-button-wrap">
      <button className={`pv-btn pv-btn--${node.variant}`} onClick={click} disabled={busy || !node.action}>
        {busy
          ? <IconLoader2 size={14} className="plugin-spin" aria-hidden />
          : node.icon && <PluginIcon pluginId={pluginId} path={node.icon} size={14} fallback={<></>} />}
        {node.label}
      </button>
      {error && <ViewError text={error} />}
    </span>
  )
}

function ViewForm({ node }: { node: Extract<ViewNode, { type: 'form' }> }) {
  const { run, pluginId } = useView()
  const [values, setValues] = useState(() => formDefaults(node.fields))
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!node.action) return
    setBusy(true)
    setError('')
    try { await run(node.action, formArgs(node.action, values)) } catch (err) { setError(pluginErrorMessage(err)) } finally { setBusy(false) }
  }
  return (
    <form className="pv-form" onSubmit={submit}>
      {node.fields.map(f => (
        <ViewFormField key={f.key} field={f} value={values[f.key]} onChange={v => setValues(prev => ({ ...prev, [f.key]: v }))} />
      ))}
      {error && <ViewError text={error} />}
      <div className="pv-form__actions">
        <PluginOrigin pluginId={pluginId} className="pv-form__origin" />
        <button className="pv-btn pv-btn--primary" type="submit" disabled={busy || !node.action}>
          {busy && <IconLoader2 size={14} className="plugin-spin" aria-hidden />}
          {node.submitLabel}
        </button>
      </div>
    </form>
  )
}

function ViewFormField({ field, value, onChange }: { field: ViewField; value: string | number | boolean; onChange: (v: string | number | boolean) => void }) {
  if (field.type === 'bool') {
    return (
      <label className="pv-check">
        <input type="checkbox" checked={value === true} onChange={e => onChange(e.target.checked)} />
        <span>{field.label}</span>
        {field.help && <small>{field.help}</small>}
      </label>
    )
  }
  const text = String(value ?? '')
  return (
    <label className="pv-field">
      <span>{field.label}{field.required ? ' *' : ''}</span>
      {field.type === 'textarea'
        ? <textarea className="input" rows={4} value={text} required={field.required} placeholder={field.placeholder} onChange={e => onChange(e.target.value)} />
        : field.type === 'select'
          ? (
            <select className="input" value={text} required={field.required} onChange={e => onChange(e.target.value)}>
              {!field.required && <option value="">—</option>}
              {field.options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
            </select>
          )
          : (
            <input
              className="input"
              type={field.type === 'number' ? 'number' : field.type === 'secret' ? 'password' : 'text'}
              autoComplete={field.type === 'secret' ? 'off' : undefined}
              value={text}
              required={field.required}
              placeholder={field.placeholder}
              onChange={e => onChange(field.type === 'number' && e.target.value !== '' ? Number(e.target.value) : e.target.value)}
            />
          )}
      {field.help && <small>{field.help}</small>}
    </label>
  )
}

export function ViewEmpty({ title, text, icon }: { title: string; text: string; icon?: string }) {
  const view = useContext(ViewContext)
  return (
    <div className="pv-empty">
      {view && icon
        ? <PluginIcon pluginId={view.pluginId} path={icon} size={28} fallback={<IconInbox size={28} stroke={1.4} />} />
        : <IconInbox size={28} stroke={1.4} aria-hidden />}
      {title && <p className="pv-empty__title">{title}</p>}
      {text && <p className="pv-empty__text">{text}</p>}
    </div>
  )
}

// A plugin's page holding its own chats: the same conversation component as the Supervisor, with
// the model fixed and its own history beside it.
function ViewConversation({ conversationId, model }: { conversationId: string; model: string }) {
  const { pluginId, projectId } = useView()
  const models = usePlugins(s => s.ui.models)
  const ui = usePlugins(s => s.ui)
  const value = conversationElementModel(pluginId, model) || models.find(m => m.pluginId === pluginId)?.value || ''
  const info = models.find(m => m.value === value)
  if (!projectId) return null
  if (!info) return <ViewEmpty title="This conversation is not available" text="The model it uses is off or no longer offered by its plugin." />
  const { source, rowId } = conversationElementSource(ui, pluginId, conversationId, info.id)
  return (
    <div className="pv-conversation">
      <OrchestratorPanel embed={{ pluginId, model: value, label: info.label, source, rowId }} />
    </div>
  )
}
