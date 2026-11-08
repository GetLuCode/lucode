import { safeHttpUrl } from './plugins.ts'

// A plugin's page is JSON that a host renderer draws with host components. Everything a plugin
// sends passes through here first: this keeps only known types, with the fields the renderer
// reads, as plain values of the right kind. Whatever else came along is gone before React sees it.

export type ViewTone = 'default' | 'muted' | 'success' | 'warning' | 'danger' | 'accent'
export interface ViewAction { rpc: string; args: Record<string, unknown>; confirm?: string }
export interface ViewField {
  key: string
  label: string
  type: 'text' | 'textarea' | 'number' | 'bool' | 'select' | 'secret'
  default?: string | number | boolean
  options: { value: string; label: string }[]
  help: string
  placeholder: string
  required: boolean
}
export interface ViewListItem { title: string; subtitle: string; badge: string; href: string | null; action: ViewAction | null; node: ViewNode | null }
export interface ViewTableColumn { key: string; label: string; align: 'left' | 'right' | 'center' }
export interface ViewChartSeries { key: string; name: string }

export type ViewNode =
  | { type: 'stack'; direction: 'row' | 'column'; gap: number; children: ViewNode[] }
  | { type: 'grid'; columns: number; children: ViewNode[] }
  | { type: 'card'; title: string; subtitle: string; icon: string; children: ViewNode[] }
  | { type: 'heading'; text: string; level: 1 | 2 | 3 | 4 }
  | { type: 'text'; text: string; tone: ViewTone }
  | { type: 'markdown'; text: string }
  | { type: 'stat'; label: string; value: string; delta: string; tone: ViewTone }
  | { type: 'badge'; text: string; tone: ViewTone }
  | { type: 'table'; columns: ViewTableColumn[]; rows: Record<string, string>[]; truncated: number }
  | { type: 'progress'; label: string; percent: number; tone: ViewTone }
  | { type: 'list'; items: ViewListItem[] }
  | { type: 'chart'; kind: 'bar' | 'line'; title: string; series: ViewChartSeries[]; rows: Record<string, string | number>[]; hiddenSeries: number }
  | { type: 'button'; label: string; icon: string; variant: 'primary' | 'secondary' | 'ghost' | 'danger'; action: ViewAction | null }
  | { type: 'form'; fields: ViewField[]; submitLabel: string; action: ViewAction | null }
  | { type: 'link'; text: string; href: string }
  | { type: 'divider' }
  | { type: 'empty'; title: string; text: string; icon: string }
  | { type: 'conversation'; conversationId: string; model: string }

const MAX_DEPTH = 24
const MAX_NODES = 4000
const MAX_ROWS = 1000
const MAX_ITEMS = 500
const MAX_SERIES = 5 // the palette's validated slots; more than this stops being readable as colors

const KNOWN_TYPES = new Set(['stack', 'grid', 'card', 'heading', 'text', 'markdown', 'stat', 'badge', 'table', 'progress', 'list', 'chart', 'button', 'form', 'link', 'divider', 'empty', 'conversation'])
const TONES = new Set<ViewTone>(['default', 'muted', 'success', 'warning', 'danger', 'accent'])

type Raw = Record<string, unknown>
const isObject = (v: unknown): v is Raw => !!v && typeof v === 'object' && !Array.isArray(v)

const text = (...values: unknown[]): string => {
  for (const v of values) {
    if (typeof v === 'string') return v
    if (typeof v === 'number' && Number.isFinite(v)) return String(v)
    if (typeof v === 'boolean') return String(v)
  }
  return ''
}
const tone = (v: unknown): ViewTone => (TONES.has(v as ViewTone) ? (v as ViewTone) : 'default')
const num = (v: unknown, fallback: number): number => {
  const n = typeof v === 'string' && v.trim() !== '' ? Number(v) : v
  return typeof n === 'number' && Number.isFinite(n) ? n : fallback
}
const clamp = (n: number, lo: number, hi: number) => Math.min(hi, Math.max(lo, n))

// JSON only: functions and the like cannot survive this, nor can a __proto__ key reach an object.
function plainArgs(v: unknown): Record<string, unknown> {
  if (!isObject(v)) return {}
  try {
    return JSON.parse(JSON.stringify(v)) as Record<string, unknown>
  } catch {
    return {}
  }
}

export function sanitizeAction(raw: unknown): ViewAction | null {
  if (!isObject(raw) || typeof raw.rpc !== 'string' || !/^[\w.-]{1,128}$/.test(raw.rpc)) return null
  const action: ViewAction = { rpc: raw.rpc, args: plainArgs(raw.args) }
  if (raw.confirm === true) action.confirm = 'Are you sure?'
  else if (typeof raw.confirm === 'string' && raw.confirm.trim()) action.confirm = raw.confirm
  return action
}

function field(raw: unknown): ViewField | null {
  if (!isObject(raw)) return null
  const key = text(raw.key, raw.name)
  if (!key) return null
  const type = ['text', 'textarea', 'number', 'bool', 'select', 'secret'].includes(raw.type as string) ? (raw.type as ViewField['type']) : 'text'
  const def = raw.default
  const options = Array.isArray(raw.options)
    ? raw.options.flatMap(o => {
        if (typeof o === 'string' || typeof o === 'number') return [{ value: String(o), label: String(o) }]
        if (isObject(o) && text(o.value)) return [{ value: text(o.value), label: text(o.label, o.value) }]
        return []
      })
    : []
  return {
    key,
    label: text(raw.label) || key,
    type,
    ...(typeof def === 'string' || typeof def === 'number' || typeof def === 'boolean' ? { default: def } : {}),
    options,
    help: text(raw.help),
    placeholder: text(raw.placeholder),
    required: raw.required === true,
  }
}

function table(raw: Raw): ViewNode {
  const declared = Array.isArray(raw.columns) ? raw.columns : []
  let columns: ViewTableColumn[] = declared.flatMap((c, i): ViewTableColumn[] => {
    if (typeof c === 'string' || typeof c === 'number') return [{ key: String(c), label: String(c), align: 'left' }]
    if (!isObject(c)) return []
    const key = text(c.key, c.name) || String(i)
    return [{ key, label: text(c.label, c.title) || key, align: c.align === 'right' || c.align === 'center' ? c.align : 'left' }]
  })
  const given = Array.isArray(raw.rows) ? raw.rows : []
  const first = given.find(isObject)
  if (columns.length === 0 && first) columns = Object.keys(first).map(k => ({ key: k, label: k, align: 'left' as const }))
  // a row is an array in column order, or an object by column key
  const rows = given.slice(0, MAX_ROWS).flatMap((row): Record<string, string>[] => {
    const out: Record<string, string> = Object.create(null) as Record<string, string>
    if (Array.isArray(row)) columns.forEach((c, i) => { out[c.key] = text(row[i]) })
    else if (isObject(row)) columns.forEach(c => { out[c.key] = text(row[c.key]) })
    else return []
    return [out]
  })
  return { type: 'table', columns, rows, truncated: Math.max(0, given.length - MAX_ROWS) }
}

function chart(raw: Raw): ViewNode {
  const given = (Array.isArray(raw.series) ? raw.series : []).filter(isObject)
  const labels = Array.isArray(raw.labels) ? raw.labels.map(l => text(l)) : []
  const series: ViewChartSeries[] = given.slice(0, MAX_SERIES).map((s, i) => ({ key: `s${i}`, name: text(s.name, s.label) || `Series ${i + 1}` }))
  // x values in the order they first appear; a point is a number (by position) or {x|label, y|value}
  const byX = new Map<string, Record<string, string | number>>()
  given.slice(0, MAX_SERIES).forEach((s, i) => {
    const points = Array.isArray(s.data) ? s.data : Array.isArray(s.points) ? s.points : []
    points.slice(0, MAX_ROWS).forEach((p, at) => {
      const x = isObject(p) ? text(p.x, p.label) || labels[at] || String(at + 1) : labels[at] || String(at + 1)
      const y = num(isObject(p) ? (p.y ?? p.value) : p, NaN)
      if (!Number.isFinite(y)) return
      if (!byX.has(x)) byX.set(x, { x })
      byX.get(x)![`s${i}`] = y
    })
  })
  return {
    type: 'chart',
    kind: raw.kind === 'line' ? 'line' : 'bar',
    title: text(raw.title),
    series,
    rows: [...byX.values()],
    hiddenSeries: Math.max(0, given.length - MAX_SERIES),
  }
}

// The tree as the renderer may draw it, or null if nothing in it can be drawn.
export function sanitizeViewTree(raw: unknown): ViewNode | null {
  let budget = MAX_NODES
  const walk = (v: unknown, depth: number): ViewNode | null => {
    if (!isObject(v) || depth > MAX_DEPTH || budget <= 0) return null
    if (typeof v.type !== 'string' || !KNOWN_TYPES.has(v.type)) return null
    budget--
    const children = () => (Array.isArray(v.children) ? v.children : []).flatMap(c => {
      const n = walk(c, depth + 1)
      return n ? [n] : []
    })
    switch (v.type) {
      case 'stack':
        return { type: 'stack', direction: v.direction === 'row' || v.direction === 'horizontal' ? 'row' : 'column', gap: clamp(num(v.gap, 12), 0, 48), children: children() }
      case 'grid':
        return { type: 'grid', columns: clamp(Math.round(num(v.columns, 2)), 1, 6), children: children() }
      case 'card':
        return { type: 'card', title: text(v.title), subtitle: text(v.subtitle), icon: text(v.icon), children: children() }
      case 'heading': {
        const level = clamp(Math.round(num(v.level, 2)), 1, 4) as 1 | 2 | 3 | 4
        return { type: 'heading', text: text(v.text, v.title), level }
      }
      case 'text':
        return { type: 'text', text: text(v.text, v.value), tone: tone(v.tone) }
      case 'markdown':
        return { type: 'markdown', text: text(v.text, v.source, v.content) }
      case 'stat':
        return { type: 'stat', label: text(v.label), value: text(v.value), delta: text(v.delta), tone: tone(v.tone) }
      case 'badge':
        return { type: 'badge', text: text(v.text, v.label), tone: tone(v.tone) }
      case 'table':
        return table(v)
      case 'progress': {
        const max = num(v.max, 100)
        return { type: 'progress', label: text(v.label), percent: max > 0 ? clamp((num(v.value, 0) / max) * 100, 0, 100) : 0, tone: tone(v.tone) }
      }
      case 'list': {
        const items = (Array.isArray(v.items) ? v.items : []).slice(0, MAX_ITEMS).flatMap((it): ViewListItem[] => {
          if (typeof it === 'string' || typeof it === 'number') return [{ title: String(it), subtitle: '', badge: '', href: null, action: null, node: null }]
          if (!isObject(it)) return []
          if (typeof it.type === 'string') {
            const node = walk(it, depth + 1)
            return node ? [{ title: '', subtitle: '', badge: '', href: null, action: null, node }] : []
          }
          return [{
            title: text(it.title, it.text, it.label),
            subtitle: text(it.subtitle, it.detail, it.description),
            badge: text(it.badge),
            href: safeHttpUrl(it.href ?? it.url),
            action: sanitizeAction(it.action),
            node: null,
          }]
        })
        return { type: 'list', items }
      }
      case 'chart':
        return chart(v)
      case 'button': {
        const variant = v.variant === 'primary' || v.variant === 'ghost' || v.variant === 'danger' ? v.variant : 'secondary'
        return { type: 'button', label: text(v.label, v.text), icon: text(v.icon), variant, action: sanitizeAction(v.action) }
      }
      case 'form': {
        const fields = (Array.isArray(v.fields) ? v.fields : []).flatMap(f => {
          const made = field(f)
          return made ? [made] : []
        })
        return { type: 'form', fields, submitLabel: text(v.submitLabel) || 'Submit', action: sanitizeAction(v.submit ?? v.action) }
      }
      case 'link': {
        const href = safeHttpUrl(v.href ?? v.url)
        const label = text(v.text, v.label)
        if (!href) return label ? { type: 'text', text: label, tone: 'default' } : null // the words stay, never the address
        return { type: 'link', text: label || href, href }
      }
      case 'divider':
        return { type: 'divider' }
      case 'empty':
        return { type: 'empty', title: text(v.title), text: text(v.text, v.description), icon: text(v.icon) }
      case 'conversation':
        return { type: 'conversation', conversationId: text(v.conversationId), model: text(v.model) }
      default:
        return null
    }
  }
  return walk(raw, 0)
}

export type RpcOutcome =
  | { kind: 'tree'; tree: ViewNode }
  | { kind: 'toast'; text: string; tone: ViewTone }
  | { kind: 'none' }

// An rpc answers with a new view tree, with {toast}, or with anything else, which the page ignores.
export function classifyRpcResult(result: unknown): RpcOutcome {
  if (!isObject(result)) return { kind: 'none' }
  if (typeof result.type === 'string') {
    const tree = sanitizeViewTree(result)
    if (tree) return { kind: 'tree', tree }
  }
  if (typeof result.toast === 'string' && result.toast) return { kind: 'toast', text: result.toast, tone: 'default' }
  if (isObject(result.toast)) {
    const said = text(result.toast.message, result.toast.text)
    if (said) return { kind: 'toast', text: said, tone: tone(result.toast.tone) }
  }
  return { kind: 'none' }
}

// What a form sends: its action's args, each value by its key, and all of them under `values`.
export function formArgs(action: ViewAction, values: Record<string, unknown>): Record<string, unknown> {
  return { ...action.args, ...values, values }
}

// The first value of each field of a form.
export function formDefaults(fields: ViewField[]): Record<string, string | number | boolean> {
  const out: Record<string, string | number | boolean> = {}
  for (const f of fields) {
    if (f.default !== undefined) out[f.key] = f.default
    else if (f.type === 'bool') out[f.key] = false
    else if (f.type === 'select' && f.options.length > 0 && f.required) out[f.key] = f.options[0].value
    else out[f.key] = ''
  }
  return out
}
