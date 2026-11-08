// What the app does with a plugin's frontend page. The page's code runs in a Worker (served by the
// hub with a policy that keeps it off the network); it never runs here. What it builds arrives as
// operations, and nothing reaches the document before it passes these lists: allowed tags,
// attributes, styles and events. No markup is ever parsed, no handler attribute is ever set, and no
// address outside the plugin's own images is ever loaded. Everything a Worker sends is untrusted,
// including messages its own runtime did not write.

export const SVG_NS = 'http://www.w3.org/2000/svg'

export const MAX_NODES = 50000
const MAX_TEXT = 1 << 20
const MAX_ATTR = 8192
const MAX_PATH_ATTR = 1 << 18
const MAX_STYLE_VALUE = 4096
export const MAX_STYLESHEET = 1 << 20

const HTML_TAGS = new Set([
  'a', 'abbr', 'address', 'article', 'aside', 'b', 'bdi', 'bdo', 'blockquote', 'br', 'button', 'caption', 'cite',
  'code', 'col', 'colgroup', 'data', 'datalist', 'dd', 'del', 'details', 'dfn', 'div', 'dl', 'dt', 'em', 'fieldset',
  'figcaption', 'figure', 'footer', 'form', 'h1', 'h2', 'h3', 'h4', 'h5', 'h6', 'header', 'hgroup', 'hr', 'i', 'img',
  'input', 'ins', 'kbd', 'label', 'legend', 'li', 'main', 'mark', 'menu', 'meter', 'nav', 'ol', 'optgroup', 'option',
  'output', 'p', 'pre', 'progress', 'q', 'rp', 'rt', 'ruby', 's', 'samp', 'search', 'section', 'select', 'small',
  'span', 'strong', 'sub', 'summary', 'sup', 'table', 'tbody', 'td', 'textarea', 'tfoot', 'th', 'thead', 'time', 'tr',
  'u', 'ul', 'var', 'wbr',
])

// No <use>, <image>, <foreignObject>, <a>, filters or animation: they load, link or run.
const SVG_TAGS = new Set([
  'svg', 'g', 'path', 'circle', 'ellipse', 'line', 'polyline', 'polygon', 'rect', 'text', 'tspan', 'textPath', 'defs',
  'linearGradient', 'radialGradient', 'stop', 'clipPath', 'mask', 'pattern', 'marker', 'symbol', 'title', 'desc',
])

const GLOBAL_ATTRS = new Set(['class', 'id', 'title', 'lang', 'dir', 'role', 'tabindex', 'hidden', 'translate', 'spellcheck', 'inputmode', 'enterkeyhint', 'autocapitalize'])

const TAG_ATTRS: Record<string, string[]> = {
  a: ['href'],
  img: ['src', 'alt', 'width', 'height', 'loading', 'decoding'],
  input: ['type', 'name', 'value', 'placeholder', 'min', 'max', 'step', 'minlength', 'maxlength', 'pattern', 'required', 'readonly', 'disabled', 'checked', 'autocomplete', 'list', 'size', 'multiple'],
  button: ['type', 'name', 'value', 'disabled'],
  label: ['for'],
  select: ['name', 'multiple', 'required', 'disabled', 'size', 'autocomplete'],
  option: ['value', 'selected', 'disabled', 'label'],
  optgroup: ['label', 'disabled'],
  textarea: ['name', 'rows', 'cols', 'placeholder', 'maxlength', 'minlength', 'readonly', 'disabled', 'required', 'wrap', 'autocomplete'],
  form: ['novalidate', 'autocomplete'],
  fieldset: ['disabled', 'name'],
  td: ['colspan', 'rowspan', 'headers'],
  th: ['colspan', 'rowspan', 'headers', 'scope', 'abbr'],
  col: ['span'],
  colgroup: ['span'],
  ol: ['start', 'reversed', 'type'],
  li: ['value'],
  progress: ['value', 'max'],
  meter: ['value', 'min', 'max', 'low', 'high', 'optimum'],
  details: ['open', 'name'],
  time: ['datetime'],
  data: ['value'],
  output: ['for', 'name'],
  del: ['datetime'],
  ins: ['datetime'],
}

const INPUT_TYPES = new Set(['text', 'search', 'email', 'url', 'tel', 'number', 'range', 'date', 'time', 'datetime-local', 'month', 'week', 'color', 'checkbox', 'radio', 'password', 'submit', 'reset', 'button', 'hidden'])
const BUTTON_TYPES = new Set(['button', 'submit', 'reset'])

const SVG_ATTRS = new Set([
  'id', 'class', 'role', 'tabindex', 'viewBox', 'width', 'height', 'x', 'y', 'x1', 'y1', 'x2', 'y2', 'cx', 'cy', 'r', 'rx', 'ry',
  'd', 'points', 'pathLength', 'transform', 'fill', 'fill-opacity', 'fill-rule', 'stroke', 'stroke-width', 'stroke-opacity',
  'stroke-linecap', 'stroke-linejoin', 'stroke-dasharray', 'stroke-dashoffset', 'stroke-miterlimit', 'opacity', 'clip-rule',
  'clip-path', 'mask', 'offset', 'stop-color', 'stop-opacity', 'gradientUnits', 'gradientTransform', 'spreadMethod', 'fx', 'fy',
  'patternUnits', 'patternContentUnits', 'patternTransform', 'clipPathUnits', 'maskUnits', 'maskContentUnits', 'markerWidth',
  'markerHeight', 'markerUnits', 'refX', 'refY', 'orient', 'marker-start', 'marker-mid', 'marker-end', 'preserveAspectRatio',
  'text-anchor', 'dominant-baseline', 'alignment-baseline', 'baseline-shift', 'font-size', 'font-family', 'font-weight',
  'font-style', 'letter-spacing', 'dx', 'dy', 'rotate', 'textLength', 'lengthAdjust', 'startOffset', 'vector-effect',
  'shape-rendering', 'xmlns', 'visibility', 'display', 'color',
])

/** Events the page may listen to. The rest never reach it. */
export const EVENT_TYPES = new Set([
  'click', 'dblclick', 'auxclick', 'contextmenu', 'mousedown', 'mouseup', 'mouseover', 'mouseout', 'mouseenter', 'mouseleave',
  'mousemove', 'pointerdown', 'pointerup', 'pointermove', 'pointerover', 'pointerout', 'pointerenter', 'pointerleave',
  'pointercancel', 'wheel', 'keydown', 'keyup', 'keypress', 'beforeinput', 'input', 'change', 'submit', 'reset', 'focus',
  'blur', 'focusin', 'focusout', 'toggle', 'scroll', 'compositionstart', 'compositionend',
])
/** Sent at most once a frame. */
export const HIGH_RATE_EVENTS = new Set(['mousemove', 'pointermove', 'wheel', 'scroll'])

export function allowedTag(tag: string, svg: boolean): boolean {
  return svg ? SVG_TAGS.has(tag) : HTML_TAGS.has(tag)
}

/** An image of the plugin's own zip: lucode-asset:<path>. */
export function assetPath(src: string): string | null {
  if (!src.startsWith('lucode-asset:')) return null
  const p = src.slice('lucode-asset:'.length)
  if (!p || p.length > 512 || p.startsWith('/') || p.includes('\\') || p.includes(':') || p.split('/').some(s => s === '' || s === '.' || s === '..')) return null
  return p
}

const DATA_IMAGE = /^data:image\/(png|jpeg|gif|webp|avif|svg\+xml)[;,]/i

/** A link the app may open in a new tab. */
export function safeLink(href: string): string | null {
  try {
    const u = new URL(href)
    return u.protocol === 'http:' || u.protocol === 'https:' || u.protocol === 'mailto:' ? u.href : null
  } catch {
    return null
  }
}

/**
 * The value to set for an attribute, or null to drop it. `kind` tells the caller what is left to do
 * for values that need the app (an image of the zip).
 */
export function cleanAttr(tag: string, svg: boolean, name: string, value: string): { value: string; asset?: string } | null {
  if (typeof name !== 'string' || typeof value !== 'string') return null
  if (/^on/i.test(name) || name === 'style') return null
  if (svg) {
    if (!SVG_ATTRS.has(name) && !/^(data|aria)-[a-z0-9_.-]+$/.test(name)) return null
    const limit = name === 'd' || name === 'points' ? MAX_PATH_ATTR : MAX_ATTR
    if (value.length > limit || !cssSafe(value)) return null
    return { value }
  }
  name = name.toLowerCase()
  if (value.length > MAX_ATTR) return null
  if (/^(data|aria)-[a-z0-9_.-]+$/.test(name)) return { value }
  if (!GLOBAL_ATTRS.has(name) && !(TAG_ATTRS[tag] ?? []).includes(name)) return null
  if (tag === 'input' && name === 'type') return INPUT_TYPES.has(value.toLowerCase()) ? { value } : { value: 'text' }
  if (tag === 'button' && name === 'type') return BUTTON_TYPES.has(value.toLowerCase()) ? { value } : { value: 'button' }
  if (tag === 'a' && name === 'href') {
    // shown and checked, never followed by the browser: a click goes through the app (see SurfaceRenderer)
    if (value.startsWith('#')) return { value }
    const link = safeLink(value)
    return link ? { value: link } : null
  }
  if (tag === 'img' && name === 'src') {
    const asset = assetPath(value)
    if (asset) return { value: '', asset }
    return DATA_IMAGE.test(value) ? { value } : null
  }
  return { value }
}

// CSS escapes and comments, gone, so that what is checked is what the browser reads
function decodeCss(text: string): string {
  return text
    .replace(/\/\*[\s\S]*?\*\//g, ' ')
    .replace(/\\([0-9a-fA-F]{1,6})[ \t\n\r\f]?/g, (_, hex: string) => {
      const n = parseInt(hex, 16)
      return n > 0 && n <= 0x10ffff ? String.fromCodePoint(n) : String.fromCharCode(0xfffd)
    })
    .replace(/\\([^\n])/g, '$1')
    .toLowerCase()
}

const LOCAL_URL = /^\s*['"]?\s*(data:(image|font)\/|data:application\/(font|x-font)|#)/

/** Whether CSS loads nothing from outside: url() only of data: images and fonts, or #ids. */
export function cssSafe(text: string): boolean {
  const css = decodeCss(text)
  if (css.includes('@import') || css.includes('javascript:') || css.includes('expression(') || css.includes('-moz-binding') || css.includes('behavior:')) return false
  if (/(image-set|cross-fade|element|src)\s*\(/.test(css) || /(^|[^a-z0-9_-])image\s*\(/.test(css)) return false
  if (/attr\s*\([^)]*url/.test(css)) return false
  const re = /url\s*\(/g
  let m: RegExpExecArray | null
  while ((m = re.exec(css))) {
    if (!LOCAL_URL.test(css.slice(m.index + m[0].length))) return false
  }
  return true
}

const STYLE_PROP = /^(--[a-zA-Z0-9_-]{1,64}|-?[a-z][a-z-]{0,63})$/

/** A style declaration's value, or null to drop it. */
export function cleanStyle(prop: string, value: string): string | null {
  if (typeof prop !== 'string' || typeof value !== 'string' || !STYLE_PROP.test(prop)) return null
  if (value.length > MAX_STYLE_VALUE || !cssSafe(value)) return null
  return value
}

/** The page's stylesheet, whole or not at all: a sheet that loads from outside is refused. */
export function cleanStylesheet(css: string): { css: string; refused?: string } {
  if (!css) return { css: '' }
  if (css.length > MAX_STYLESHEET) return { css: '', refused: 'its stylesheet is larger than 1 MB' }
  if (!cssSafe(css)) return { css: '', refused: 'its stylesheet loads something from outside the plugin (url(), @import or image-set), which pages cannot do' }
  return { css }
}

// ---- operations ----------------------------------------------------------------------------

const isId = (v: unknown): v is number => typeof v === 'number' && Number.isInteger(v) && v >= 0 && v < 2 ** 31
const isStr = (v: unknown, max: number): v is string => typeof v === 'string' && v.length <= max

/** The DOM the renderer needs: a real Document, or a small fake in tests. */
export interface SurfaceDoc {
  createElement(tag: string): Element
  createElementNS(ns: string, tag: string): Element
  createTextNode(text: string): Text
}

export interface RendererHooks {
  /** An image of the zip: the app fetches it with its token and gives back an address to show. */
  asset(path: string): Promise<string | null>
  /** The page listens to an event type for the first time. */
  listen(type: string): void
  /** Whether the page may take the focus now. */
  mayFocus(): boolean
}

/**
 * Applies a Worker's operations to the page's area. Returns an error to show when the page broke a
 * limit; anything malformed is skipped.
 */
export class SurfaceRenderer {
  private nodes = new Map<number, Node>()
  private svg = new WeakSet<Node>()
  private ids = new WeakMap<Node, number>()
  private wantAsset = new WeakMap<Element, string>()
  private hooks: RendererHooks
  private doc: SurfaceDoc

  constructor(root: Element, doc: SurfaceDoc, hooks: RendererHooks) {
    this.nodes.set(0, root)
    this.ids.set(root, 0)
    this.doc = doc
    this.hooks = hooks
  }

  /** The page's id of a node, for events: the nearest one it made. */
  idOf(node: Node | null): number | null {
    for (let n = node; n; n = n.parentNode) {
      const id = this.ids.get(n)
      if (id !== undefined) return id
    }
    return null
  }

  get size() { return this.nodes.size }

  apply(ops: unknown): string | null {
    if (!Array.isArray(ops)) return null
    for (const op of ops) {
      if (!Array.isArray(op) || typeof op[0] !== 'string') continue
      const err = this.one(op)
      if (err) return err
    }
    return null
  }

  private el(id: unknown): Element | null {
    if (!isId(id)) return null
    const n = this.nodes.get(id)
    return n && n.nodeType === 1 ? (n as Element) : null
  }

  private add(id: number, node: Node): string | null {
    if (id === 0 || this.nodes.has(id)) return null // ids are the page's: never reuse a live one
    if (this.nodes.size >= MAX_NODES) return `the page has more than ${MAX_NODES} elements`
    this.nodes.set(id, node)
    this.ids.set(node, id)
    return null
  }

  private forget(node: Node) {
    const id = this.ids.get(node)
    if (id !== undefined && id !== 0) this.nodes.delete(id)
    for (let c = node.firstChild; c; c = c.nextSibling) this.forget(c)
  }

  private one(op: unknown[]): string | null {
    switch (op[0]) {
      case 'el': {
        const [, id, tag, ns] = op
        if (!isId(id) || !isStr(tag, 64)) return null
        const svg = ns === 1
        let node: Element
        if (allowedTag(tag, svg)) {
          node = svg ? this.doc.createElementNS(SVG_NS, tag) : this.doc.createElement(tag)
        } else {
          // a stand-in, so that what goes inside still has somewhere to go, and is never shown
          node = this.doc.createElement('span')
          node.setAttribute('hidden', '')
          node.setAttribute('data-lucode-refused', tag.replace(/[^a-zA-Z0-9-]/g, ''))
        }
        if (svg) this.svg.add(node)
        return this.add(id, node)
      }
      case 'text': {
        const [, id, text] = op
        if (!isId(id) || !isStr(text, MAX_TEXT)) return null
        return this.add(id, this.doc.createTextNode(text))
      }
      case 'data': {
        const [, id, text] = op
        if (!isId(id) || !isStr(text, MAX_TEXT)) return null
        const n = this.nodes.get(id)
        if (n && n.nodeType === 3) (n as Text).data = text
        return null
      }
      case 'insert': {
        const [, pid, cid, bid] = op
        const parent = this.el(pid)
        if (!parent || !isId(cid) || cid === 0) return null
        const child = this.nodes.get(cid)
        if (!child || child.contains(parent)) return null
        let before: Node | null = null
        if (isId(bid) && bid !== 0) {
          before = this.nodes.get(bid) ?? null
          if (!before || before.parentNode !== parent) before = null
        }
        parent.insertBefore(child, before)
        return null
      }
      case 'remove': {
        const [, id] = op
        if (!isId(id) || id === 0) return null
        const n = this.nodes.get(id)
        if (!n) return null
        n.parentNode?.removeChild(n)
        this.forget(n)
        return null
      }
      case 'attr': {
        const [, id, name, value] = op
        const el = this.el(id)
        if (!el || id === 0 || !isStr(name, 128)) return null
        if (el.hasAttribute('data-lucode-refused')) return null
        if (value === null) {
          if (/^on/i.test(name) || name === 'style') return null
          try { el.removeAttribute(name) } catch { /* not a valid name */ }
          if (name === 'src') this.wantAsset.delete(el)
          return null
        }
        if (!isStr(value, MAX_PATH_ATTR)) return null
        const tag = el.localName
        const clean = cleanAttr(tag, this.svg.has(el), name, value)
        if (!clean) return null
        if (clean.asset) {
          const want = clean.asset
          this.wantAsset.set(el, want)
          void this.hooks.asset(want).then(url => {
            if (url && this.wantAsset.get(el) === want) el.setAttribute('src', url)
          })
          return null
        }
        if (tag === 'img' && name === 'src') this.wantAsset.delete(el)
        try { el.setAttribute(name, clean.value) } catch { /* not a valid name */ }
        return null
      }
      case 'style': {
        const [, id, prop, value] = op
        const el = this.el(id)
        if (!el || id === 0 || !isStr(prop, 80)) return null
        const style = (el as HTMLElement).style
        if (!style) return null
        if (value === null) {
          style.removeProperty(prop)
          return null
        }
        if (!isStr(value, MAX_STYLE_VALUE)) return null
        const clean = cleanStyle(prop, value)
        if (clean !== null) style.setProperty(prop, clean)
        return null
      }
      case 'prop': {
        const [, id, name, value] = op
        const el = this.el(id) as (Element & { value?: string; checked?: boolean; selected?: boolean }) | null
        if (!el || id === 0) return null
        const tag = el.localName
        if (name === 'value' && isStr(value, MAX_TEXT) && (tag === 'input' || tag === 'textarea' || tag === 'select' || tag === 'option' || tag === 'progress' || tag === 'meter' || tag === 'button' || tag === 'data' || tag === 'li' || tag === 'output')) {
          if (el.value !== value) el.value = value
        } else if (name === 'checked' && typeof value === 'boolean' && tag === 'input') {
          el.checked = value
        } else if (name === 'selected' && typeof value === 'boolean' && tag === 'option') {
          el.selected = value
        }
        return null
      }
      case 'listen': {
        const [, type] = op
        if (isStr(type, 40) && EVENT_TYPES.has(type)) this.hooks.listen(type)
        return null
      }
      case 'focus':
      case 'blur': {
        const el = this.el(op[1]) as HTMLElement | null
        if (!el || op[1] === 0) return null
        if (op[0] === 'blur') el.blur?.()
        else if (this.hooks.mayFocus()) el.focus?.({ preventScroll: false })
        return null
      }
    }
    return null
  }
}

/** What an event tells the page: plain values only. */
export function eventDetail(e: Event, rootRect?: { left: number; top: number }): Record<string, unknown> {
  const d: Record<string, unknown> = {}
  const pick = (k: string) => {
    const v = (e as unknown as Record<string, unknown>)[k]
    if (typeof v === 'string' || typeof v === 'number' || typeof v === 'boolean') d[k] = v
  }
  for (const k of ['key', 'code', 'altKey', 'ctrlKey', 'metaKey', 'shiftKey', 'repeat', 'isComposing', 'button', 'buttons', 'detail',
    'pointerType', 'pointerId', 'pressure', 'deltaX', 'deltaY', 'deltaMode', 'inputType', 'data', 'offsetX', 'offsetY', 'newState', 'oldState']) pick(k)
  const ce = e as MouseEvent
  if (typeof ce.clientX === 'number' && rootRect) {
    d.clientX = ce.clientX - rootRect.left
    d.clientY = ce.clientY - rootRect.top
  }
  return d
}

/** Toast tones the page may use. */
export function toastTone(t: unknown): 'default' | 'muted' | 'success' | 'warning' | 'danger' | 'accent' {
  return t === 'muted' || t === 'success' || t === 'warning' || t === 'danger' || t === 'accent' ? t : 'default'
}
