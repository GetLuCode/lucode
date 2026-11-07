import { create } from 'zustand'
import * as api from '../api'
import { bumpWebhookTick, emptyPluginUI, mergeContributions, pluginErrorMessage, webhookToastDue } from '../lib/plugins'
import { classifyRpcResult, type ViewNode, type ViewTone } from '../lib/pluginView'
import type { InstalledPlugin, PluginCatalogEntry, PluginUI } from '../types'

export interface PluginToast { id: number; text: string; tone: ViewTone; pluginId: string }

interface PluginsState {
  ui: PluginUI
  installed: InstalledPlugin[]
  catalog: PluginCatalogEntry[]
  available: boolean // the hub has the plugin routes; an older one simply has no contributions
  loaded: boolean // the first answer came, so a missing plugin really is missing
  version: number // bumped on every load, so lists and images fetch again
  // per plugin, bumped when a webhook reaches it: its open page reads itself again
  webhookTick: Record<string, number>
  toasts: PluginToast[]
  result: { pluginId: string; title: string; tree: ViewNode } | null // what a chat or card action answered
  load: () => Promise<void>
  // A webhook reached a plugin (the hub sent plugin_webhook over the websocket).
  noteWebhook: (pluginId: string) => void
  toast: (text: string, tone?: ViewTone, pluginId?: string) => void
  dismissToast: (id: number) => void
  closeResult: () => void
  // Runs an action from a menu (chat or card): the answer shows up as a toast or a dialog.
  runAction: (pluginId: string, rpc: string, projectId: string | null, args: Record<string, unknown>, title: string) => Promise<void>
}

let loadSeq = 0
let toastSeq = 0

const lastWebhookToast: Record<string, number> = {}

export const usePlugins = create<PluginsState>((set, get) => ({
  ui: emptyPluginUI(),
  installed: [],
  catalog: [],
  available: false,
  loaded: false,
  version: 0,
  webhookTick: {},
  toasts: [],
  result: null,

  noteWebhook: pluginId => {
    set(s => ({ webhookTick: bumpWebhookTick(s.webhookTick, pluginId) }))
    const now = Date.now()
    if (!webhookToastDue(lastWebhookToast[pluginId], now)) return
    lastWebhookToast[pluginId] = now
    // no name in the text: the toast is framed with the plugin's name (PluginHost) because it carries pluginId
    get().toast('a webhook arrived', 'default', pluginId)
  },

  load: async () => {
    const seq = ++loadSeq
    const [ui, installed, catalog] = await Promise.allSettled([api.getPluginUI(), api.listPlugins(), api.listPluginCatalog()])
    if (seq !== loadSeq) return
    const uiAvailable = ui.status === 'fulfilled'
    // a passing failure does not take away what was already working
    if (!uiAvailable && get().available) return
    set(s => ({
      ui: uiAvailable ? mergeContributions(ui.value) : emptyPluginUI(),
      installed: installed.status === 'fulfilled' ? installed.value : s.installed,
      catalog: catalog.status === 'fulfilled' ? catalog.value : s.catalog,
      available: uiAvailable,
      loaded: true,
      version: s.version + 1,
    }))
  },

  toast: (text, tone = 'default', pluginId = '') => {
    const id = ++toastSeq
    set(s => ({ toasts: [...s.toasts.slice(-3), { id, text, tone, pluginId }] }))
    setTimeout(() => get().dismissToast(id), tone === 'danger' ? 9000 : 4500)
  },
  dismissToast: id => set(s => ({ toasts: s.toasts.filter(t => t.id !== id) })),
  closeResult: () => set({ result: null }),

  runAction: async (pluginId, rpc, projectId, args, title) => {
    try {
      const outcome = classifyRpcResult(await api.callPluginRpc(pluginId, rpc, projectId, args))
      if (outcome.kind === 'tree') set({ result: { pluginId, title, tree: outcome.tree } })
      else if (outcome.kind === 'toast') get().toast(outcome.text, outcome.tone, pluginId)
      else get().toast(`${title}: done`, 'default', pluginId)
    } catch (e) {
      get().toast(`${title}: ${pluginErrorMessage(e)}`, 'danger', pluginId)
    }
  },
}))
