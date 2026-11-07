import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'
import {
  continueConversationPath,
  conversationElementModel,
  conversationElementSource,
  conversationsOfModel,
  effectiveRisk,
  emptyPluginUI,
  groupByPlugin,
  groupPermissionsByTarget,
  httpStatusOf,
  isPluginModel,
  isSafeAssetType,
  mergeContributions,
  missingGrants,
  newPermissionKeys,
  nodeTypeDisabled,
  normalizeModelId,
  parsePluginModel,
  pluginErrorMessage,
  pluginModelId,
  pluginOfNodeType,
  runnerPairing,
  isBrowserPermission,
  pluginOriginLabel,
  pluginTargets,
  runnerPolicyProblem,
  safeAssetPath,
  safeColor,
  safeHttpUrl,
  sortByRisk,
  targetsLabel,
  unknownNodesMessage,
} from '../src/lib/plugins.ts'
import { classifyRpcResult, formArgs, formDefaults, sanitizeAction, sanitizeViewTree, type ViewNode } from '../src/lib/pluginView.ts'
import { pluginMarkdownToHtml } from '../src/lib/safeMarkdown.ts'
import type { InstalledPlugin, PluginManifest, PluginPermission } from '../src/types.ts'

const manifest = (id: string, extra: Partial<PluginManifest> = {}): PluginManifest => ({ id, name: id, version: '1.0.0', ...extra })
const row = (id: string, extra: Partial<InstalledPlugin> = {}): InstalledPlugin => ({ manifest: manifest(id), status: 'ok', enabled: true, ...extra })
const GEMINI = 'plugin:lucode.gemini/aimode'

test('the old id of Google AI Mode becomes the plugin id, and nothing else changes', () => {
  assert.equal(normalizeModelId('local:aimode'), GEMINI)
  assert.equal(normalizeModelId(GEMINI), GEMINI)
  assert.equal(normalizeModelId('sonnet'), 'sonnet')
  assert.equal(normalizeModelId('ollama:llama3'), 'ollama:llama3')
  assert.equal(normalizeModelId('local:other'), 'local:other')
})

test('a plugin model is told apart from every other model', () => {
  assert.equal(isPluginModel('local:aimode'), true)
  assert.equal(isPluginModel('plugin:acme.hello/x'), true)
  for (const m of ['sonnet', 'MiniMax-M2.7', 'ollama:a', 'local:other', 'pluginish']) assert.equal(isPluginModel(m), false, m)
})

test('a plugin model id round-trips and rejects malformed ones', () => {
  assert.equal(pluginModelId('acme.hello', 'x'), 'plugin:acme.hello/x')
  assert.deepEqual(parsePluginModel('plugin:acme.hello/x'), { pluginId: 'acme.hello', modelId: 'x' })
  assert.deepEqual(parsePluginModel('local:aimode'), { pluginId: 'lucode.gemini', modelId: 'aimode' })
  assert.equal(parsePluginModel('plugin:/x'), null)
  assert.equal(parsePluginModel('plugin:acme/'), null)
  assert.equal(parsePluginModel('sonnet'), null)
})

test('only http and https become links', () => {
  assert.equal(safeHttpUrl('https://a.example/x?y=1'), 'https://a.example/x?y=1')
  assert.equal(safeHttpUrl('HTTP://a.example/'), 'http://a.example/')
  for (const bad of ['javascript:alert(1)', 'JaVaScRiPt:alert(1)', ' javascript:alert(1)', 'data:text/html,<script>', 'vbscript:x', 'file:///etc/passwd', '/relative', '//evil.example', 'https://', '', null, 42, {}]) {
    assert.equal(safeHttpUrl(bad), null, String(bad))
  }
})

test('a color reaches the page only as plain hex', () => {
  assert.equal(safeColor('#4285f4'), '#4285f4')
  assert.equal(safeColor('#abc'), '#abc')
  for (const bad of ['red;background:url(x)', 'url(https://evil)', '#12', '#xyzxyz', 'var(--x)', 7, null]) assert.equal(safeColor(bad), undefined, String(bad))
})

test('an asset path stays inside the plugin and is an image', () => {
  assert.equal(safeAssetPath('assets/icon.svg'), 'assets/icon.svg')
  assert.equal(safeAssetPath('assets/my icon.png'), 'assets/my%20icon.png')
  for (const bad of ['../secret.png', 'a/../../b.png', '/etc/x.png', 'a\\b.png', 'C:/x.png', 'main.js', 'a//b.png', 'a.png?x=1', '', null]) {
    assert.equal(safeAssetPath(bad), null, String(bad))
  }
  assert.equal(isSafeAssetType('image/svg+xml'), true)
  assert.equal(isSafeAssetType('image/png; charset=x'), true)
  assert.equal(isSafeAssetType('text/html'), false)
})

test('contributions: bad entries drop, duplicates collapse, the wire id is worked out', () => {
  const ui = mergeContributions({
    models: [
      { pluginId: 'a.b', id: 'm', label: 'M', accent: 'url(evil)', images: true, loader: 'skeleton' },
      { pluginId: 'a.b', id: 'm', label: 'again' },
      { id: 'no-plugin' },
      { pluginId: 'c.d' },
      'junk',
    ],
    pages: [{ plugin_id: 'a.b', id: 'main', label: 'Main', render: 'renderMain' }],
    chatActions: [{ pluginId: 'a.b', id: 'x' }, { pluginId: 'a.b', id: 'y', rpc: 'go' }],
    conversations: 'nope',
  })
  assert.equal(ui.models.length, 1)
  assert.equal(ui.models[0].value, 'plugin:a.b/m')
  assert.equal(ui.models[0].label, 'M')
  assert.equal(ui.models[0].accent, undefined)
  assert.equal(ui.models[0].images, true)
  assert.equal(ui.pages[0].pluginId, 'a.b')
  assert.deepEqual(ui.chatActions.map(a => a.id), ['y'], 'an action with no rpc does nothing, so it is not offered')
  assert.deepEqual(ui.conversations, [])
  assert.deepEqual(mergeContributions(null), emptyPluginUI())
  assert.deepEqual(mergeContributions('x'), emptyPluginUI())
})

test('a model finds its conversation list, or gets a plain one of its plugin', () => {
  const ui = mergeContributions({
    models: [{ pluginId: 'a.b', id: 'm', label: 'M' }, { pluginId: 'a.b', id: 'n', label: 'N' }],
    conversations: [{ pluginId: 'a.b', id: 'hist', model: 'm', title: 'History', emptyText: 'Empty', showUrl: true }],
  })
  assert.equal(conversationsOfModel(ui, ui.models[0]).title, 'History')
  const plain = conversationsOfModel(ui, ui.models[1])
  assert.equal(plain.title, 'N conversations')
  assert.equal(plain.id, '')
  assert.equal(plain.showUrl, false)
})

test('a page conversation element names a list, or a saved conversation, and only its own plugin', () => {
  const ui = mergeContributions({ conversations: [{ pluginId: 'a.b', id: 'hist', model: 'm', title: 'History' }] })
  assert.deepEqual(conversationElementSource(ui, 'a.b', 'hist', 'm').rowId, '')
  assert.equal(conversationElementSource(ui, 'a.b', 'hist', 'm').source.id, 'hist')
  assert.equal(conversationElementSource(ui, 'a.b', 'row-1', 'm').rowId, 'row-1')
  assert.equal(conversationElementSource(ui, 'a.b', 'row-1', 'm').source.id, 'hist')
  assert.equal(conversationElementModel('a.b', 'm'), 'plugin:a.b/m')
  assert.equal(conversationElementModel('a.b', 'plugin:a.b/m'), 'plugin:a.b/m')
  assert.equal(conversationElementModel('a.b', 'plugin:other.one/m'), '', 'a page cannot use another plugin’s model')
  assert.equal(conversationElementModel('a.b', ''), '')
})

test('errors read as the hub said them', () => {
  assert.equal(pluginErrorMessage(new Error('POST /api/x → 400: plain words')), 'plain words')
  assert.equal(pluginErrorMessage(new Error('POST /api/x → 400: {"error":"nope"}')), 'nope')
  assert.equal(pluginErrorMessage(new Error('Failed to fetch')), 'Failed to fetch')
  assert.equal(httpStatusOf(new Error('GET /x → 403: no')), 403)
  assert.equal(httpStatusOf(new Error('Failed to fetch')), 0)
})

test('permissions are grouped by target, riskiest first, and exec is always high', () => {
  const perms: PluginPermission[] = [
    { key: 'storage', label: 'Storage', risk: 'low', detail: '' },
    { key: 'runner.fs', label: 'Files', risk: 'medium', detail: '', target: 'runner' },
    { key: 'runner.exec', label: 'Commands', risk: 'low', detail: '', target: 'runner' },
  ]
  const groups = groupPermissionsByTarget(perms)
  assert.deepEqual(groups.map(g => g.label), ['On the server', 'On runners'])
  assert.deepEqual(sortByRisk(groups[1].items).map(p => p.key), ['runner.exec', 'runner.fs'])
  assert.equal(effectiveRisk(perms[2]), 'high')
  assert.equal(effectiveRisk({ key: 'exec', risk: 'medium' }), 'high')
  assert.equal(effectiveRisk({ key: 'runner.exec:go', risk: 'medium' }), 'high')
  assert.equal(effectiveRisk({ key: 'executor', risk: 'low' }), 'low')
  assert.equal(effectiveRisk({ key: 'server.http:exec.example.com', risk: 'medium' }), 'medium', 'a host is not a command')
  assert.deepEqual(groupPermissionsByTarget([perms[0]]).map(g => g.target), ['server'])
})

test('install needs every requested permission ticked', () => {
  const perms: PluginPermission[] = [{ key: 'a', label: '', risk: 'low', detail: '' }, { key: 'b', label: '', risk: 'high', detail: '' }]
  assert.deepEqual(missingGrants(perms, new Set(['a'])).map(p => p.key), ['b'])
  assert.deepEqual(missingGrants(perms, new Set(['a', 'b'])), [])
  assert.deepEqual([...newPermissionKeys(['a', { key: 'b' }, 3])], ['a', 'b'])
  assert.equal(newPermissionKeys(undefined).size, 0)
})

test('where a plugin runs', () => {
  assert.deepEqual(pluginTargets(row('a.b')), ['server'], 'an old manifest is server only')
  assert.deepEqual(pluginTargets(row('a.b', { targets: { runner: {} } })), ['runner'])
  assert.deepEqual(pluginTargets(row('a.b', { targets: { server: {}, runner: {} } })), ['server', 'runner'])
  assert.deepEqual(pluginTargets({ manifest: manifest('a.b', { targets: { runner: {} } }) }), ['runner'])
  assert.equal(targetsLabel(['server']), 'Server')
  assert.equal(targetsLabel(['runner']), 'Runners')
  assert.equal(targetsLabel(['server', 'runner']), 'Server and runners')
})

test('a runner policy problem says what to change', () => {
  const runs = row('a.b', { targets: { runner: { permissions: { exec: { commands: ['go'] } } } } })
  const reads = row('a.b', { targets: { runner: { permissions: { fs: 'read' } } } })
  const on = (policy: string) => ({ policy, hostname: 'kali', runnerId: 'r1' })
  assert.match(runnerPolicyProblem(runs, on('standard')), /kali.*--plugin-policy=full/s)
  assert.equal(runnerPolicyProblem(runs, on('full')), '')
  assert.equal(runnerPolicyProblem(reads, on('standard')), '')
  assert.match(runnerPolicyProblem(reads, on('off')), /refuses all plugins.*--plugin-policy=standard/s)
})

test('plugin nodes know their plugin, and whether it is on', () => {
  assert.equal(pluginOfNodeType('plugin.acme.hello.ask'), 'acme.hello')
  assert.equal(pluginOfNodeType('action.run_agent'), '')
  const installed = [row('on.one'), row('off.one', { enabled: false })]
  assert.equal(nodeTypeDisabled({}, installed), false)
  assert.equal(nodeTypeDisabled({ plugin_id: 'on.one' }, installed), false)
  assert.equal(nodeTypeDisabled({ plugin_id: 'off.one' }, installed), true)
  assert.equal(nodeTypeDisabled({ plugin_id: 'gone.one' }, installed), true)
  assert.equal(nodeTypeDisabled({ plugin_id: 'on.one', plugin_disabled: true }, installed), true)
  assert.match(unknownNodesMessage(['plugin.acme.hello.ask']), /acme\.hello/)
  assert.match(unknownNodesMessage(['action.new']), /Restart the hub/)
})

test('groups keep the order items first appear in', () => {
  const groups = groupByPlugin([{ p: 'b', n: 1 }, { p: 'a', n: 2 }, { p: 'b', n: 3 }], x => x.p)
  assert.deepEqual(groups.map(g => [g.pluginId, g.items.length]), [['b', 2], ['a', 1]])
})

// ---- view trees

const only = <T extends ViewNode['type']>(n: ViewNode | null, type: T) => {
  assert.equal(n?.type, type)
  return n as Extract<ViewNode, { type: T }>
}

test('unknown types are dropped, wherever they are', () => {
  assert.equal(sanitizeViewTree({ type: 'script', src: 'x' }), null)
  assert.equal(sanitizeViewTree('text'), null)
  assert.equal(sanitizeViewTree(null), null)
  assert.equal(sanitizeViewTree({ type: '__proto__' }), null)
  assert.equal(sanitizeViewTree({ type: 'constructor' }), null)
  const s = only(sanitizeViewTree({ type: 'stack', children: [{ type: 'iframe', src: 'x' }, { type: 'text', text: 'ok' }, 'junk', null] }), 'stack')
  assert.deepEqual(s.children.map(c => c.type), ['text'])
})

test('only the fields the renderer reads survive, as plain values', () => {
  const t = sanitizeViewTree({ type: 'text', text: 'hi', tone: 'neon', dangerouslySetInnerHTML: { __html: '<img onerror=x>' }, style: { background: 'url(x)' }, className: 'x', onClick: 'y' })
  assert.deepEqual(t, { type: 'text', text: 'hi', tone: 'default' })
  const s = only(sanitizeViewTree({ type: 'stat', label: 'L', value: 12, delta: { evil: 1 }, tone: 'success' }), 'stat')
  assert.equal(s.value, '12')
  assert.equal(s.delta, '')
  assert.equal(s.tone, 'success')
})

test('a link is http or https, or its words stay without the address', () => {
  assert.deepEqual(sanitizeViewTree({ type: 'link', href: 'https://a.example/', text: 'A' }), { type: 'link', text: 'A', href: 'https://a.example/' })
  assert.deepEqual(sanitizeViewTree({ type: 'link', href: 'javascript:alert(1)', text: 'A' }), { type: 'text', text: 'A', tone: 'default' })
  assert.equal(sanitizeViewTree({ type: 'link', href: 'javascript:alert(1)' }), null)
  const list = only(sanitizeViewTree({ type: 'list', items: [{ title: 'x', href: 'javascript:alert(1)' }, { title: 'y', url: 'https://a.example/' }, 'plain'] }), 'list')
  assert.equal(list.items[0].href, null)
  assert.equal(list.items[1].href, 'https://a.example/')
  assert.equal(list.items[2].title, 'plain')
})

test('an action names an rpc and carries plain data', () => {
  assert.equal(sanitizeAction({ rpc: '../../evil' }), null)
  assert.equal(sanitizeAction({ rpc: 12 }), null)
  assert.equal(sanitizeAction('go'), null)
  assert.deepEqual(sanitizeAction({ rpc: 'go', args: { a: 1 }, confirm: true }), { rpc: 'go', args: { a: 1 }, confirm: 'Are you sure?' })
  assert.deepEqual(sanitizeAction({ rpc: 'go', args: 'x', confirm: 'Sure?' }), { rpc: 'go', args: {}, confirm: 'Sure?' })
  const b = only(sanitizeViewTree({ type: 'button', label: 'Go', action: { rpc: 'a b' }, variant: 'neon' }), 'button')
  assert.equal(b.action, null, 'a button with a bad action draws disabled')
  assert.equal(b.variant, 'secondary')
})

test('a deep or huge tree is cut off instead of hanging the page', () => {
  let deep: unknown = { type: 'text', text: 'bottom' }
  for (let i = 0; i < 2000; i++) deep = { type: 'stack', children: [deep] }
  const out = sanitizeViewTree(deep)
  assert.equal(out?.type, 'stack')
  let depth = 0
  for (let n = out as ViewNode | undefined; n?.type === 'stack'; n = n.children[0]) depth++
  assert.ok(depth <= 25, String(depth))

  const wide = sanitizeViewTree({ type: 'stack', children: Array.from({ length: 10000 }, () => ({ type: 'divider' })) })
  assert.ok(only(wide, 'stack').children.length < 5000)
})

test('numbers from a plugin stay in range', () => {
  assert.equal(only(sanitizeViewTree({ type: 'grid', columns: 99 }), 'grid').columns, 6)
  assert.equal(only(sanitizeViewTree({ type: 'grid', columns: 'x' }), 'grid').columns, 2)
  assert.equal(only(sanitizeViewTree({ type: 'stack', gap: 9999 }), 'stack').gap, 48)
  assert.equal(only(sanitizeViewTree({ type: 'heading', text: 'h', level: 42 }), 'heading').level, 4)
  assert.equal(only(sanitizeViewTree({ type: 'progress', value: 150 }), 'progress').percent, 100)
  assert.equal(only(sanitizeViewTree({ type: 'progress', value: 5, max: 10 }), 'progress').percent, 50)
  assert.equal(only(sanitizeViewTree({ type: 'progress', value: 5, max: 0 }), 'progress').percent, 0)
})

test('table rows come as arrays or objects, and cells are text', () => {
  const t = only(sanitizeViewTree({ type: 'table', columns: ['a', { key: 'b', label: 'B', align: 'right' }], rows: [[1, 'x'], { a: 2, b: { evil: 1 } }, 'junk'] }), 'table')
  assert.deepEqual(t.columns.map(c => c.label), ['a', 'B'])
  assert.equal(t.rows.length, 2)
  assert.equal(t.rows[0].a, '1')
  assert.equal(t.rows[1].b, '')
  const inferred = only(sanitizeViewTree({ type: 'table', rows: [{ x: 1 }] }), 'table')
  assert.deepEqual(inferred.columns.map(c => c.key), ['x'])
})

test('a chart lines its series up by x and holds to the palette’s size', () => {
  const c = only(sanitizeViewTree({ type: 'chart', kind: 'line', labels: ['jan', 'feb'], series: [{ name: 'a', data: [1, 2] }, { name: 'b', data: [{ x: 'feb', y: 5 }, { x: 'jan', y: 'bad' }] }] }), 'chart')
  assert.equal(c.kind, 'line')
  assert.deepEqual(c.rows, [{ x: 'jan', s0: 1 }, { x: 'feb', s0: 2, s1: 5 }])
  const many = only(sanitizeViewTree({ type: 'chart', kind: 'pie', series: Array.from({ length: 8 }, (_, i) => ({ name: String(i), data: [1] })) }), 'chart')
  assert.equal(many.kind, 'bar')
  assert.equal(many.series.length, 5)
  assert.equal(many.hiddenSeries, 3)
})

test('forms: fields are typed, and submit sends values with the action’s args', () => {
  const f = only(sanitizeViewTree({
    type: 'form',
    fields: [
      { key: 'q', label: 'Q', type: 'text', required: true, default: 'x' },
      { name: 'on', type: 'bool' },
      { key: 'pick', type: 'select', options: ['a', { value: 'b', label: 'B' }], required: true },
      { key: 'odd', type: 'html' },
      { label: 'no key' },
    ],
    submit: { rpc: 'go', args: { id: 1 } },
  }), 'form')
  assert.deepEqual(f.fields.map(x => x.key), ['q', 'on', 'pick', 'odd'])
  assert.equal(f.fields[3].type, 'text')
  assert.deepEqual(formDefaults(f.fields), { q: 'x', on: false, pick: 'a', odd: '' })
  assert.deepEqual(formArgs(f.action!, { q: 'y' }), { id: 1, q: 'y', values: { q: 'y' } })
})

test('an rpc answer is a tree, a toast, or nothing the page shows', () => {
  assert.deepEqual(classifyRpcResult({ type: 'text', text: 'a' }), { kind: 'tree', tree: { type: 'text', text: 'a', tone: 'default' } })
  assert.deepEqual(classifyRpcResult({ toast: 'done' }), { kind: 'toast', text: 'done', tone: 'default' })
  assert.deepEqual(classifyRpcResult({ toast: { message: 'bad', tone: 'danger' } }), { kind: 'toast', text: 'bad', tone: 'danger' })
  for (const r of [null, 3, 'x', [], {}, { type: 'marquee' }, { toast: '' }]) assert.deepEqual(classifyRpcResult(r), { kind: 'none' }, JSON.stringify(r))
})

// ---- markdown

test('markdown from a plugin never carries raw HTML, scripts or other schemes', () => {
  const dirty = pluginMarkdownToHtml([
    '<script>alert(1)</script>',
    '',
    'hi <img src=x onerror=alert(1)> there',
    '',
    '[bad](javascript:alert(1)) [ok](https://a.example/x)',
    '',
    '![pic](https://a.example/p.png) ![bad](javascript:x)',
    '',
    '<iframe src="https://evil.example"></iframe>',
  ].join('\n'))
  assert.doesNotMatch(dirty, /<script/i)
  assert.doesNotMatch(dirty, /<img/i)
  assert.doesNotMatch(dirty, /<iframe/i)
  assert.doesNotMatch(dirty, /href="javascript/i)
  assert.match(dirty, /&lt;script&gt;/)
  assert.match(dirty, /<a href="https:\/\/a\.example\/x" target="_blank" rel="noreferrer noopener">ok<\/a>/)
  assert.match(dirty, /<a href="https:\/\/a\.example\/p\.png"[^>]*>pic<\/a>/, 'an image is a link, never a fetch')
})

test('ordinary markdown still reads as markdown', () => {
  const html = pluginMarkdownToHtml('# Title\n\n**bold** `code`\n\n- a\n- b\n\n```js\nif (a < b) {}\n```')
  assert.match(html, /<h1>Title<\/h1>/)
  assert.match(html, /<strong>bold<\/strong>/)
  assert.match(html, /<li>a<\/li>/)
  assert.match(html, /a &lt; b/)
})

test('a chat of a plugin\'s page says so when it comes back; the panel\'s does not', () => {
  assert.equal(continueConversationPath('lucode.gemini', 'c 1', 'page'), '/api/plugins/lucode.gemini/conversations/c%201/continue?surface=page')
  assert.equal(continueConversationPath('lucode.gemini', 'c1'), '/api/plugins/lucode.gemini/conversations/c1/continue')
})

test('a plugin cannot pass for the Supervisor in the picker', () => {
  const ui = mergeContributions({ models: [{ pluginId: 'evil.one', pluginName: 'Evil', id: 'm', mode: 'Supervisor', hint: 'Claude, on a runner' }] })
  assert.equal(ui.models[0].mode, 'Supervisor', 'its words are its own')
  assert.equal(pluginOriginLabel(ui.models[0]), 'Plugin: Evil', 'and lucode says whose they are')
  assert.equal(pluginOriginLabel({ pluginId: 'evil.one', pluginName: '' }), 'Plugin: evil.one', 'no name is no escape')
})

test('the engine\'s name never reaches the UI', () => {
  const name = ['g', 'o', 'j', 'a'].join('')
  const walk = (dir: string): string[] => readdirSync(dir).flatMap(f => {
    const p = join(dir, f)
    return statSync(p).isDirectory() ? walk(p) : [p]
  })
  const src = fileURLToPath(new URL('../src', import.meta.url))
  const hits = walk(src).filter(f => /\.(ts|tsx|js|css|html)$/.test(f) && readFileSync(f, 'utf8').toLowerCase().includes(name))
  assert.deepEqual(hits, [])
})

test('a browser permission is high risk whatever it says', () => {
  assert.equal(isBrowserPermission('server.browser:https://www.google.com'), true)
  assert.equal(isBrowserPermission('server.http:browser.example.com'), false)
  assert.equal(effectiveRisk({ key: 'server.browser:https://www.google.com', risk: 'medium' }), 'high')
})

test('pairing shows only where the hub knows about it', () => {
  assert.equal(runnerPairing({}), 'unknown', 'an older hub')
  assert.equal(runnerPairing({ paired: true }), 'paired')
  assert.equal(runnerPairing({ paired: false }), 'unpaired')
})
