import { test } from 'node:test'
import assert from 'node:assert/strict'
import {
  assetPath,
  cleanAttr,
  cleanStyle,
  cleanStylesheet,
  cssSafe,
  MAX_NODES,
  safeLink,
  SurfaceRenderer,
  type SurfaceDoc,
} from '../src/lib/pluginSurface.ts'

// ---- a DOM small enough to see what the renderer does --------------------------------------

class FakeNode {
  nodeType: number
  parentNode: FakeNode | null = null
  childNodes: FakeNode[] = []
  constructor(type: number) { this.nodeType = type }
  get firstChild() { return this.childNodes[0] ?? null }
  get nextSibling() {
    const p = this.parentNode
    if (!p) return null
    return p.childNodes[p.childNodes.indexOf(this) + 1] ?? null
  }
  contains(n: FakeNode | null): boolean {
    for (let x = n; x; x = x.parentNode) if (x === this) return true
    return false
  }
  insertBefore(child: FakeNode, ref: FakeNode | null) {
    if (child.parentNode) child.parentNode.removeChild(child)
    const at = ref ? this.childNodes.indexOf(ref) : this.childNodes.length
    this.childNodes.splice(at, 0, child)
    child.parentNode = this
    return child
  }
  removeChild(child: FakeNode) {
    this.childNodes.splice(this.childNodes.indexOf(child), 1)
    child.parentNode = null
    return child
  }
}
class FakeText extends FakeNode {
  data: string
  constructor(data: string) { super(3); this.data = data }
}
class FakeElement extends FakeNode {
  localName: string
  ns: string
  attrs = new Map<string, string>()
  styles = new Map<string, string>()
  value = ''
  checked = false
  selected = false
  focused = 0
  style = {
    setProperty: (k: string, v: string) => { this.styles.set(k, v) },
    removeProperty: (k: string) => { this.styles.delete(k) },
  }
  constructor(tag: string, ns = 'html') { super(1); this.localName = tag; this.ns = ns }
  setAttribute(k: string, v: string) { this.attrs.set(k, v) }
  getAttribute(k: string) { return this.attrs.get(k) ?? null }
  hasAttribute(k: string) { return this.attrs.has(k) }
  removeAttribute(k: string) { this.attrs.delete(k) }
  focus() { this.focused++ }
  blur() {}
}
const doc = {
  createElement: (tag: string) => new FakeElement(tag),
  createElementNS: (ns: string, tag: string) => new FakeElement(tag, ns),
  createTextNode: (text: string) => new FakeText(text),
} as unknown as SurfaceDoc

function setup(opts: { focus?: boolean } = {}) {
  const root = new FakeElement('div')
  const listened: string[] = []
  const assets: string[] = []
  const r = new SurfaceRenderer(root as unknown as Element, doc, {
    asset: async p => { assets.push(p); return `blob:fake/${p}` },
    listen: t => { listened.push(t) },
    mayFocus: () => opts.focus ?? true,
  })
  return { root, r, listened, assets }
}
const kid = (n: FakeNode, i = 0) => n.childNodes[i] as FakeElement

// ---- the lists -----------------------------------------------------------------------------

test('handler attributes, inline style and unknown attributes never get through', () => {
  assert.equal(cleanAttr('div', false, 'onclick', 'alert(1)'), null)
  assert.equal(cleanAttr('div', false, 'ONCLICK', 'alert(1)'), null)
  assert.equal(cleanAttr('div', false, 'style', 'color:red'), null)
  assert.equal(cleanAttr('div', false, 'srcdoc', 'x'), null)
  assert.equal(cleanAttr('form', false, 'action', 'https://evil.example'), null)
  assert.equal(cleanAttr('button', false, 'formaction', 'https://evil.example'), null)
  assert.equal(cleanAttr('div', false, 'popover', ''), null, 'the top layer would leave the page area')
  assert.deepEqual(cleanAttr('div', false, 'class', 'card'), { value: 'card' })
  assert.deepEqual(cleanAttr('div', false, 'data-id', '7'), { value: '7' })
  assert.deepEqual(cleanAttr('div', false, 'aria-label', 'x'), { value: 'x' })
})

test('links are http, https or mailto, and images come from the zip or data:', () => {
  assert.equal(cleanAttr('a', false, 'href', 'javascript:alert(1)'), null)
  assert.equal(cleanAttr('a', false, 'href', ' JaVaScRiPt:alert(1)'), null)
  assert.equal(cleanAttr('a', false, 'href', 'data:text/html,<b>x</b>'), null)
  assert.deepEqual(cleanAttr('a', false, 'href', 'https://example.com/a'), { value: 'https://example.com/a' })
  assert.deepEqual(cleanAttr('a', false, 'href', '#section'), { value: '#section' })
  assert.equal(cleanAttr('img', false, 'src', 'https://tracker.example/p.gif'), null, 'an outside image would carry data out')
  assert.equal(cleanAttr('img', false, 'src', '//tracker.example/p.gif'), null)
  assert.deepEqual(cleanAttr('img', false, 'src', 'data:image/png;base64,AAAA'), { value: 'data:image/png;base64,AAAA' })
  assert.deepEqual(cleanAttr('img', false, 'src', 'lucode-asset:assets/a.png'), { value: '', asset: 'assets/a.png' })
  assert.equal(assetPath('lucode-asset:../secret'), null)
  assert.equal(assetPath('lucode-asset:/etc/passwd'), null)
  assert.equal(assetPath('lucode-asset:a//b.png'), null)
  assert.equal(safeLink('ftp://x'), null)
})

test('input and button types stay within what a page needs', () => {
  assert.deepEqual(cleanAttr('input', false, 'type', 'file'), { value: 'text' })
  assert.deepEqual(cleanAttr('input', false, 'type', 'image'), { value: 'text' })
  assert.deepEqual(cleanAttr('input', false, 'type', 'checkbox'), { value: 'checkbox' })
  assert.deepEqual(cleanAttr('button', false, 'type', 'menu'), { value: 'button' })
})

test('CSS loads nothing from outside, however it is written', () => {
  assert.equal(cssSafe('background: url(https://evil.example/x)'), false)
  assert.equal(cssSafe('background: URL( "https://evil.example/x" )'), false)
  assert.equal(cssSafe('background: \\75 rl(https://evil.example/x)'), false, 'an escaped url( is still url(')
  assert.equal(cssSafe('background: u\\rl(https://evil.example/x)'), false)
  assert.equal(cssSafe('background-image: image-set("x.png" 1x)'), false)
  assert.equal(cssSafe('@import "https://evil.example/a.css";'), false)
  assert.equal(cssSafe('@\\69mport "https://evil.example/a.css";'), false)
  assert.equal(cssSafe('content: attr(data-x url)'), false)
  assert.equal(cssSafe('background: url(data:image/png;base64,AAAA)'), true)
  assert.equal(cssSafe('mask: url(#m)'), true)
  assert.equal(cssSafe('content: "\\f101"; color: var(--lc-accent)'), true)
  assert.equal(cssSafe('.curly { color: red }'), true, 'a word with "url" in it is not url(')
  assert.equal(cleanStyle('color', 'red'), 'red')
  assert.equal(cleanStyle('--lc-gap', '8px'), '8px')
  assert.equal(cleanStyle('background', 'url(https://x)'), null)
  assert.equal(cleanStyle('color; background', 'red'), null)
})

test('a stylesheet goes whole, or not at all with the reason', () => {
  assert.deepEqual(cleanStylesheet('.a { color: red }'), { css: '.a { color: red }' })
  const bad = cleanStylesheet('.a { color: red } .b { background: url(https://evil.example) }')
  assert.equal(bad.css, '')
  assert.match(bad.refused ?? '', /outside the plugin/)
  assert.match(cleanStylesheet('x'.repeat((1 << 20) + 1)).refused ?? '', /1 MB/)
})

// ---- the renderer --------------------------------------------------------------------------

test('a tag that is not allowed becomes a hidden stand-in, and nothing set on it sticks', () => {
  const { root, r } = setup()
  r.apply([['el', 1, 'script', 0], ['attr', 1, 'src', 'https://evil.example/x.js'], ['text', 2, 'alert(1)'], ['insert', 1, 2, 0], ['insert', 0, 1, 0]])
  const el = kid(root)
  assert.equal(el.localName, 'span')
  assert.ok(el.hasAttribute('hidden'))
  assert.equal(el.getAttribute('data-lucode-refused'), 'script')
  assert.equal(el.hasAttribute('src'), false)
  for (const tag of ['iframe', 'object', 'embed', 'style', 'link', 'meta', 'base', 'template', 'dialog', 'video', 'audio', 'canvas']) {
    const s = setup()
    s.r.apply([['el', 1, tag, 0], ['insert', 0, 1, 0]])
    assert.equal(kid(s.root).localName, 'span', tag)
  }
})

test('svg keeps its shapes, but not what loads or links', () => {
  const { root, r } = setup()
  r.apply([
    ['el', 1, 'svg', 1], ['el', 2, 'path', 1], ['el', 3, 'use', 1], ['el', 4, 'foreignObject', 1],
    ['attr', 2, 'd', 'M0 0L10 10'], ['attr', 2, 'fill', 'url(https://evil.example/#g)'], ['attr', 2, 'stroke', 'url(#g)'],
    ['attr', 1, 'viewBox', '0 0 10 10'], ['attr', 1, 'onload', 'x()'],
    ['insert', 1, 2, 0], ['insert', 1, 3, 0], ['insert', 1, 4, 0], ['insert', 0, 1, 0],
  ])
  const svg = kid(root)
  assert.equal(svg.ns, 'http://www.w3.org/2000/svg')
  assert.equal(svg.getAttribute('viewBox'), '0 0 10 10')
  assert.equal(svg.hasAttribute('onload'), false)
  const path = kid(svg, 0)
  assert.equal(path.getAttribute('d'), 'M0 0L10 10')
  assert.equal(path.hasAttribute('fill'), false)
  assert.equal(path.getAttribute('stroke'), 'url(#g)')
  assert.equal(kid(svg, 1).localName, 'span')
  assert.equal(kid(svg, 2).localName, 'span')
})

test('the tree follows the operations: insert, move, text, remove', () => {
  const { root, r } = setup()
  r.apply([['el', 1, 'ul', 0], ['el', 2, 'li', 0], ['el', 3, 'li', 0], ['text', 4, 'a'], ['text', 5, 'b'],
    ['insert', 2, 4, 0], ['insert', 3, 5, 0], ['insert', 1, 2, 0], ['insert', 1, 3, 0], ['insert', 0, 1, 0]])
  const ul = kid(root)
  assert.deepEqual(ul.childNodes.map(n => (n.childNodes[0] as FakeText).data), ['a', 'b'])
  r.apply([['insert', 1, 3, 2]]) // b before a
  assert.deepEqual(ul.childNodes.map(n => (n.childNodes[0] as FakeText).data), ['b', 'a'])
  r.apply([['data', 4, 'A']])
  assert.equal((kid(ul, 1).childNodes[0] as FakeText).data, 'A')
  r.apply([['remove', 3]])
  assert.equal(ul.childNodes.length, 1)
  assert.equal(r.size, 4, 'a removed node and what was under it are forgotten')
  r.apply([['attr', 5, 'class', 'x'], ['insert', 1, 3, 0]])
  assert.equal(ul.childNodes.length, 1, 'a forgotten id does nothing')
})

test('malformed or hostile operations are skipped, never thrown', () => {
  const { root, r } = setup()
  r.apply([['el', 1, 'div', 0], ['insert', 0, 1, 0], ['el', 2, 'div', 0], ['insert', 1, 2, 0]])
  assert.equal(r.apply('nope'), null)
  assert.equal(r.apply([null, 3, ['nothing'], ['el', -1, 'div'], ['el', 1.5, 'div'], ['el', 9, { x: 1 }], ['attr', 1, 'class', 7]]), null)
  r.apply([['insert', 2, 1, 0]]) // a node inside its own child
  assert.equal(kid(root).localName, 'div')
  assert.equal(kid(root).childNodes.length, 1)
  r.apply([['insert', 1, 0, 0]]) // the root inside something
  assert.equal(root.parentNode, null)
  r.apply([['el', 1, 'span', 0]]) // an id that is in use
  assert.equal(kid(root).localName, 'div')
  r.apply([['attr', 0, 'class', 'x'], ['style', 0, 'display', 'none'], ['remove', 0]])
  assert.equal(root.hasAttribute('class'), false, 'the area itself is the app\'s')
  assert.equal(root.styles.size, 0)
})

test('a page with too many elements is stopped', () => {
  const { r } = setup()
  const ops: unknown[] = []
  for (let i = 1; i <= MAX_NODES + 5; i++) ops.push(['text', i, ''])
  assert.match(r.apply(ops) ?? '', /more than/)
})

test('styles, props, events and focus go through their lists', () => {
  const { root, r, listened } = setup({ focus: false })
  r.apply([['el', 1, 'input', 0], ['insert', 0, 1, 0],
    ['style', 1, 'color', 'red'], ['style', 1, 'background', 'url(https://evil.example)'],
    ['prop', 1, 'value', 'hi'], ['prop', 1, 'checked', true], ['prop', 1, 'innerHTML', '<b>'],
    ['listen', 'click'], ['listen', 'beforeunload'], ['listen', 'message'], ['focus', 1]])
  const input = kid(root)
  assert.equal(input.styles.get('color'), 'red')
  assert.equal(input.styles.has('background'), false)
  assert.equal(input.value, 'hi')
  assert.equal(input.checked, true)
  assert.equal((input as unknown as Record<string, unknown>).innerHTML, undefined)
  assert.deepEqual(listened, ['click'])
  assert.equal(input.focused, 0, 'a page does not take the focus from the app')
})

test('an image of the zip is fetched by the app, and only the latest one shows', async () => {
  const { root, r, assets } = setup()
  r.apply([['el', 1, 'img', 0], ['insert', 0, 1, 0], ['attr', 1, 'src', 'lucode-asset:assets/a.png'], ['attr', 1, 'src', 'lucode-asset:assets/b.png']])
  await new Promise(res => setTimeout(res, 0))
  assert.deepEqual(assets, ['assets/a.png', 'assets/b.png'])
  assert.equal(kid(root).getAttribute('src'), 'blob:fake/assets/b.png')
})
