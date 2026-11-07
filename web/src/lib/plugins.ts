import type {
  FlowNodeType,
  InstalledPlugin,
  PluginConversationContribution,
  PluginModelContribution,
  PluginPermission,
  PluginRunnerInfo,
  PluginTarget,
  PluginUI,
} from '../types'

// The pure logic of the plugin system: what the web does with what the hub sends. It imports no
// React and no browser API, so tests run it directly.

// ---- Model ids -------------------------------------------------------------------------------

const PLUGIN_PREFIX = 'plugin:'
// The old id of Google AI Mode: saved choices and old sessions keep working.
const LEGACY_LOCAL_AIMODE = 'local:aimode'
const GEMINI_PLUGIN_ID = 'lucode.gemini'
const GEMINI_MODEL = `${PLUGIN_PREFIX}${GEMINI_PLUGIN_ID}/aimode`

export const normalizeModelId = (model: string): string => (model === LEGACY_LOCAL_AIMODE ? GEMINI_MODEL : model)
export const isPluginModel = (model: string): boolean => normalizeModelId(model).startsWith(PLUGIN_PREFIX)
export const pluginModelId = (pluginId: string, modelId: string): string => `${PLUGIN_PREFIX}${pluginId}/${modelId}`

export function parsePluginModel(model: string): { pluginId: string; modelId: string } | null {
  const id = normalizeModelId(model)
  if (!id.startsWith(PLUGIN_PREFIX)) return null
  const rest = id.slice(PLUGIN_PREFIX.length)
  const slash = rest.indexOf('/')
  if (slash <= 0 || slash === rest.length - 1) return null
  return { pluginId: rest.slice(0, slash), modelId: rest.slice(slash + 1) }
}

// ---- Safe values from a plugin ---------------------------------------------------------------

// Only http and https ever become a link. Anything else (javascript:, data:, a relative path) is
// dropped, and the caller shows the text without a link.
export function safeHttpUrl(raw: unknown): string | null {
  if (typeof raw !== 'string') return null
  const text = raw.trim()
  if (!/^https?:\/\//i.test(text)) return null
  try {
    const u = new URL(text)
    return u.protocol === 'http:' || u.protocol === 'https:' ? u.href : null
  } catch {
    return null
  }
}

// A color reaches a style attribute only as a plain hex value.
export function safeColor(raw: unknown): string | undefined {
  return typeof raw === 'string' && /^#(?:[0-9a-f]{3,4}|[0-9a-f]{6}|[0-9a-f]{8})$/i.test(raw.trim()) ? raw.trim() : undefined
}

const ASSET_EXT = /\.(?:png|svg|webp|jpe?g)$/i

// The address of a plugin's image under /assets/, or null if the path could leave the folder.
export function safeAssetPath(raw: unknown): string | null {
  if (typeof raw !== 'string' || !raw || !ASSET_EXT.test(raw)) return null
  if (/[\\:?#\0]/.test(raw) || raw.startsWith('/')) return null
  const parts = raw.split('/')
  if (parts.some(p => !p || p === '.' || p === '..')) return null
  return parts.map(encodeURIComponent).join('/')
}

const ASSET_TYPES = new Set(['image/png', 'image/svg+xml', 'image/webp', 'image/jpeg'])
// A blob becomes an <img> only with an image type of the allowlist (an svg inside <img> runs nothing).
export const isSafeAssetType = (type: string): boolean => ASSET_TYPES.has(type.split(';')[0].trim().toLowerCase())

// ---- Contributions ---------------------------------------------------------------------------

export const emptyPluginUI = (): PluginUI => ({ models: [], conversations: [], pages: [], chatActions: [], cardActions: [] })

const str = (v: unknown): string => (typeof v === 'string' ? v : '')
const objects = (v: unknown): Record<string, unknown>[] =>
  Array.isArray(v) ? v.filter((x): x is Record<string, unknown> => !!x && typeof x === 'object' && !Array.isArray(x)) : []

// Each list as the rest of the web wants it: only entries that say which plugin they are from,
// one per plugin and id, and the model's id on the wire worked out.
export function mergeContributions(raw: unknown): PluginUI {
  const r = (raw && typeof raw === 'object' ? raw : {}) as Record<string, unknown>
  function list<T>(key: keyof PluginUI, make: (o: Record<string, unknown>, pluginId: string, id: string) => T): T[] {
    const seen = new Set<string>()
    const out: T[] = []
    for (const o of objects(r[key])) {
      const pluginId = str(o.pluginId) || str(o.plugin_id)
      const id = str(o.id)
      if (!pluginId || !id || seen.has(`${pluginId}/${id}`)) continue
      seen.add(`${pluginId}/${id}`)
      out.push(make(o, pluginId, id))
    }
    return out
  }
  return {
    models: list('models', (o, pluginId, id) => ({
      pluginId,
      id,
      value: pluginModelId(pluginId, id),
      label: str(o.label) || id,
      mode: str(o.mode) || str(o.label) || id,
      hint: str(o.hint),
      icon: str(o.icon),
      accent: safeColor(o.accent),
      images: o.images === true,
      loader: str(o.loader),
      waitingText: str(o.waitingText),
      pluginName: str(o.pluginName) || str(o.plugin_name),
    })),
    conversations: list('conversations', (o, pluginId, id) => ({
      pluginId,
      id,
      model: str(o.model),
      title: str(o.title) || 'Conversations',
      emptyText: str(o.emptyText) || 'None yet.',
      showUrl: o.showUrl === true,
    })),
    pages: list('pages', (o, pluginId, id) => ({
      pluginId,
      id,
      label: str(o.label) || id,
      icon: str(o.icon),
      render: str(o.render),
    })),
    chatActions: list('chatActions', (o, pluginId, id) => ({ pluginId, id, label: str(o.label) || id, icon: str(o.icon), rpc: str(o.rpc) })).filter(a => a.rpc),
    cardActions: list('cardActions', (o, pluginId, id) => ({ pluginId, id, label: str(o.label) || id, icon: str(o.icon), rpc: str(o.rpc) })).filter(a => a.rpc),
  }
}

// The conversation list of a model: the one its plugin contributes for it, or else every
// conversation of that plugin under a plain title.
export function conversationsOfModel(ui: PluginUI, model: PluginModelContribution): PluginConversationContribution {
  return (
    ui.conversations.find(c => c.pluginId === model.pluginId && c.model === model.id) ?? {
      pluginId: model.pluginId,
      id: '',
      model: model.id,
      title: `${model.label} conversations`,
      emptyText: 'None yet.',
      showUrl: false,
    }
  )
}

// Which conversation a page's `conversation` element means: a contribution of the plugin by id,
// or a saved conversation (its row id) inside the plugin's list.
export function conversationElementSource(ui: PluginUI, pluginId: string, conversationId: string, modelId: string): { source: PluginConversationContribution; rowId: string } {
  const named = ui.conversations.find(c => c.pluginId === pluginId && c.id === conversationId)
  if (named) return { source: named, rowId: '' }
  const byModel = ui.conversations.find(c => c.pluginId === pluginId && c.model === modelId)
  return {
    source: byModel ?? { pluginId, id: '', model: modelId, title: 'Conversations', emptyText: 'None yet.', showUrl: false },
    rowId: conversationId,
  }
}

// A `conversation` element may name its model by its id in the manifest or by its full id.
export function conversationElementModel(pluginId: string, model: string): string {
  const full = parsePluginModel(model)
  if (full) return full.pluginId === pluginId ? pluginModelId(full.pluginId, full.modelId) : ''
  return model ? pluginModelId(pluginId, model) : ''
}

// Groups by plugin, in first-seen order, for the lists that show a heading per plugin.
export function groupByPlugin<T>(items: T[], pluginOf: (t: T) => string): { pluginId: string; items: T[] }[] {
  const out = new Map<string, T[]>()
  for (const it of items) {
    const id = pluginOf(it)
    if (!out.has(id)) out.set(id, [])
    out.get(id)!.push(it)
  }
  return [...out].map(([pluginId, list]) => ({ pluginId, items: list }))
}

// Who a plugin's entry comes from, always in lucode's words: a plugin picks its mode and hint, and
// could call them "Supervisor" and "Claude, on a runner".
export const pluginOriginLabel = (m: Pick<PluginModelContribution, 'pluginName' | 'pluginId'>): string =>
  `Plugin: ${m.pluginName || m.pluginId}`

// `page`: the chat belongs to the plugin's page. Without it, a chat that is gone comes back in the panel.
export const continueConversationPath = (pluginId: string, conversationId: string, surface?: 'page'): string =>
  `/api/plugins/${encodeURIComponent(pluginId)}/conversations/${encodeURIComponent(conversationId)}/continue${surface ? `?surface=${surface}` : ''}`

// ---- Errors ----------------------------------------------------------------------------------

// api.ts raises `METHOD /path → 403: body`. The person reads the body, and the hub's own words if
// the body is JSON.
export function pluginErrorMessage(e: unknown): string {
  const text = e instanceof Error ? e.message : String(e)
  const m = /→ \d{3}: ([\s\S]*)$/.exec(text)
  const body = (m ? m[1] : text).trim()
  if (body.startsWith('{')) {
    try {
      const j = JSON.parse(body) as { error?: unknown; message?: unknown }
      const said = typeof j.error === 'string' ? j.error : typeof j.message === 'string' ? j.message : ''
      if (said) return said
    } catch { /* not JSON after all */ }
  }
  return body || 'Something went wrong.'
}

export function httpStatusOf(e: unknown): number {
  const m = /→ (\d{3}):/.exec(e instanceof Error ? e.message : String(e))
  return m ? Number(m[1]) : 0
}

// ---- Permissions, grants and targets ---------------------------------------------------------

export const groupPermissionsByTarget = (permissions: PluginPermission[]): { target: PluginTarget; label: string; items: PluginPermission[] }[] => {
  const groups: { target: PluginTarget; label: string; items: PluginPermission[] }[] = [
    { target: 'server', label: 'On the server', items: permissions.filter(p => (p.target ?? 'server') === 'server') },
    { target: 'runner', label: 'On runners', items: permissions.filter(p => p.target === 'runner') },
  ]
  return groups.filter(g => g.items.length > 0)
}

export const isBrowserPermission = (key: string): boolean => /^(server\.)?browser(:|$)/.test(key)

// Whether pairing applies to this row: a hub from before pairing does not send it.
export const runnerPairing = (r: Pick<PluginRunnerInfo, 'paired'>): 'paired' | 'unpaired' | 'unknown' =>
  r.paired === undefined ? 'unknown' : r.paired ? 'paired' : 'unpaired'

// Running commands on a runner, and scripts in a browser, are high risk whatever a manifest or an older hub says.
export function effectiveRisk(p: Pick<PluginPermission, 'key' | 'risk' | 'target'>): string {
  return /^(runner\.)?exec(:|$)/.test(p.key) || isBrowserPermission(p.key) ? 'high' : p.risk
}

const RISK_ORDER: Record<string, number> = { high: 0, medium: 1, low: 2 }
export const sortByRisk = (permissions: PluginPermission[]): PluginPermission[] =>
  [...permissions].sort((a, b) => (RISK_ORDER[effectiveRisk(a)] ?? 3) - (RISK_ORDER[effectiveRisk(b)] ?? 3))

// Install is only offered once everything the plugin asks for is ticked.
export const missingGrants = (permissions: PluginPermission[], granted: ReadonlySet<string>): PluginPermission[] =>
  permissions.filter(p => !granted.has(p.key))

// Inspect names the new ones by key, or by the whole permission.
export function newPermissionKeys(raw: unknown): Set<string> {
  if (!Array.isArray(raw)) return new Set()
  return new Set(raw.flatMap(x => (typeof x === 'string' ? [x] : x && typeof (x as { key?: unknown }).key === 'string' ? [(x as { key: string }).key] : [])))
}

// Where a plugin's code runs. A plugin with no targets is an old one: server only.
export function pluginTargets(p: Pick<InstalledPlugin, 'targets' | 'manifest'>): PluginTarget[] {
  const row = p.targets as Record<string, unknown> | undefined
  const declared = p.manifest?.targets as Record<string, unknown> | undefined
  const out: PluginTarget[] = []
  if (row?.server || declared?.server) out.push('server')
  if (row?.runner || declared?.runner) out.push('runner')
  return out.length ? out : ['server']
}

export function targetsLabel(targets: PluginTarget[]): string {
  if (targets.length === 2) return 'Server and runners'
  return targets[0] === 'runner' ? 'Runners' : 'Server'
}

function runnerPermissionsOf(p: Pick<InstalledPlugin, 'targets' | 'manifest'>): Record<string, unknown> {
  return p.manifest?.targets?.runner?.permissions ?? p.targets?.runner?.permissions ?? {}
}

// What stands between a runner and this plugin, said so the operator knows what to change.
export function runnerPolicyProblem(p: Pick<InstalledPlugin, 'targets' | 'manifest'>, runner: Pick<PluginRunnerInfo, 'policy' | 'hostname' | 'runnerId'>): string {
  const name = runner.hostname || runner.runnerId
  if (runner.policy === 'off') {
    return `${name} refuses all plugins. To allow this one, restart its lucode-runner with --plugin-policy=standard, or --plugin-policy=full if it needs to run commands.`
  }
  if (runner.policy === 'standard' && runnerPermissionsOf(p).exec) {
    return `This plugin runs commands, and ${name} does not allow that, so those calls fail there. To allow it, restart its lucode-runner with --plugin-policy=full.`
  }
  return ''
}

// ---- Flow nodes ------------------------------------------------------------------------------

// plugin.<pluginId>.<nodeId>: the plugin id has dots of its own, the node id does not.
export function pluginOfNodeType(type: string): string {
  if (!type.startsWith('plugin.')) return ''
  const rest = type.slice('plugin.'.length)
  const dot = rest.lastIndexOf('.')
  return dot > 0 ? rest.slice(0, dot) : ''
}

// A node stays listed when its plugin is off, so the editor never loses it, but it says so.
export function nodeTypeDisabled(t: Pick<FlowNodeType, 'plugin_id' | 'plugin_disabled'>, installed: Pick<InstalledPlugin, 'enabled' | 'manifest'>[]): boolean {
  if (!t.plugin_id) return false
  if (t.plugin_disabled) return true
  const row = installed.find(p => p.manifest?.id === t.plugin_id)
  return !row || !row.enabled
}

// What the banner says about nodes the canvas cannot draw.
export function unknownNodesMessage(types: string[]): string {
  const plugins = [...new Set(types.map(pluginOfNodeType).filter(Boolean))]
  if (plugins.length === types.length && plugins.length > 0) {
    return `This flow uses nodes of a plugin that is not installed (${plugins.join(', ')}), so they are not drawn and nothing here is saved. Install the plugin to edit it.`
  }
  return `This flow uses nodes the running hub does not know (${types.join(', ')}), so they are not drawn and nothing here is saved. Restart the hub, then reload this page.`
}
