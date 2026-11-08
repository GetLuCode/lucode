import { useEffect, useRef, useState } from 'react'
import { IconAlertTriangle } from '@tabler/icons-react'
import * as api from '../../api'
import { isSafeAssetType, pluginErrorMessage } from '../../lib/plugins'
import { cleanStylesheet, eventDetail, HIGH_RATE_EVENTS, safeLink, SurfaceRenderer, toastTone } from '../../lib/pluginSurface'
import { usePlugins } from '../../store/plugins'

// The base of every page's area: the app's theme as variables the page's own CSS can use, and
// nothing of the app's own styles. The area is contained (see .plugin-surface in Plugins.css), so
// nothing a page draws, fixed or not, leaves it.
const BASE_CSS = `
:host {
  all: initial;
  display: block;
  --lc-bg: var(--bg, #0c0c0e);
  --lc-surface: var(--bg-panel, #111114);
  --lc-hover: var(--bg-hover, #17171b);
  --lc-input: var(--bg-input, #16161a);
  --lc-border: var(--border-bright, #2e2e35);
  --lc-text: var(--text, #e8e4dd);
  --lc-muted: var(--text-muted, #6b6771);
  --lc-accent: var(--accent, #00d4aa);
  --lc-on-accent: #0c0c0e;
  --lc-danger: var(--error, #ff5f5f);
  --lc-warning: var(--warning, #f59e0b);
  --lc-radius: var(--radius, 8px);
  --lc-font: var(--font-sans, system-ui, sans-serif);
  --lc-mono: var(--font-mono, monospace);
  color: var(--lc-text);
  font: 14px/1.5 var(--lc-font);
  color-scheme: dark;
}
.lc-root { display: block; min-height: 100%; box-sizing: border-box; }
.lc-root *, .lc-root *::before, .lc-root *::after { box-sizing: border-box; }
code, pre, kbd, samp { font-family: var(--lc-mono); }
img, svg { max-width: 100%; }
`

const MAX_PENDING_RPCS = 32
const MAX_MESSAGES_PER_SECOND = 5000

type Status = { kind: 'loading' } | { kind: 'running' } | { kind: 'failed'; message: string }

// A plugin's own frontend: its code in a Worker, what it draws in a shadow root of this area, and
// its only way out the rpcs plugin.json lists for the page.
export function PluginSurface({ pluginId, pageId, projectId }: { pluginId: string; pageId: string; projectId: string | null }) {
  const hostRef = useRef<HTMLDivElement>(null)
  const workerRef = useRef<Worker | null>(null)
  const [status, setStatus] = useState<Status>({ kind: 'loading' })
  const [notice, setNotice] = useState('')
  const webhookTick = usePlugins(s => s.webhookTick[pluginId] ?? 0)

  useEffect(() => {
    const host = hostRef.current
    if (!host) return
    let cancelled = false
    let worker: Worker | null = null
    const cleanups: (() => void)[] = []
    const assetUrls: string[] = []
    setStatus({ kind: 'loading' })
    setNotice('')

    const fail = (message: string) => {
      if (cancelled) return
      worker?.terminate()
      worker = null
      workerRef.current = null
      setStatus({ kind: 'failed', message })
    }

    api.getPluginPageUI(pluginId, pageId, projectId).then(ui => {
      if (cancelled) return
      const url = api.pluginScriptUrl(ui.script)
      if (!url) {
        fail('A plugin page with its own frontend only opens on this instance, not through a remote one.')
        return
      }
      const allowed = new Set(ui.rpc)

      // the area: a shadow root, so the app's styles and the page's never meet (the page's code is
      // in the Worker and cannot reach it; open only so that a remount can reuse it)
      const shadow = host.shadowRoot ?? host.attachShadow({ mode: 'open' })
      shadow.replaceChildren()
      const base = document.createElement('style')
      base.textContent = BASE_CSS
      shadow.append(base)
      const sheet = cleanStylesheet(ui.style ?? '')
      if (sheet.refused) setNotice(`The page's styles are off: ${sheet.refused}.`)
      if (sheet.css) {
        const own = document.createElement('style')
        own.textContent = sheet.css
        shadow.append(own)
      }
      const root = document.createElement('div')
      root.className = 'lc-root'
      shadow.append(root)

      const listened = new Set<string>()
      let lastUserEvent = 0
      const renderer = new SurfaceRenderer(root, document, {
        asset: async path => {
          try {
            const blob = await api.fetchPluginAsset(pluginId, path.split('/').map(encodeURIComponent).join('/'))
            if (!isSafeAssetType(blob.type)) return null
            const u = URL.createObjectURL(blob)
            assetUrls.push(u)
            return u
          } catch {
            return null
          }
        },
        listen: type => {
          if (listened.has(type)) return
          listened.add(type)
          root.addEventListener(type, onEvent, { capture: true })
          cleanups.push(() => root.removeEventListener(type, onEvent, { capture: true }))
        },
        // a page takes the focus only from itself, or right after someone used it
        mayFocus: () => {
          const active = document.activeElement
          return !active || active === document.body || active === host || Date.now() - lastUserEvent < 1000
        },
      })

      // ---- events: the page decides what they do, the browser's defaults stay with the app
      let evSeq = 0
      const links = new Map<number, string>()
      const frame = new Map<string, { e: Event; target: number }>()
      let frameScheduled = false

      const send = (e: Event, target: number) => {
        const seq = ++evSeq
        const t = e.target as HTMLInputElement | null
        let sync: Record<string, unknown> | undefined
        if (t && (t.localName === 'input' || t.localName === 'textarea' || t.localName === 'select')) {
          sync = { value: String(t.value ?? '') }
          if (t.localName === 'input' && (t.type === 'checkbox' || t.type === 'radio')) sync.checked = !!t.checked
        }
        if (e.type === 'click') {
          const a = (t as Element | null)?.closest?.('a[href]')
          const href = a && root.contains(a) ? a.getAttribute('href') : null
          if (href) links.set(seq, href)
        }
        worker?.postMessage({ lucode: 1, t: 'ev', seq, target, type: e.type, detail: eventDetail(e, root.getBoundingClientRect()), sync })
      }

      function onEvent(e: Event) {
        if (!worker) return
        if (e.isTrusted) lastUserEvent = Date.now()
        // the app's page must never follow a link or submit a form of the page's
        if (e.type === 'submit') e.preventDefault()
        if (e.type === 'click' && (e.target as Element | null)?.closest?.('a[href]')) e.preventDefault()
        const target = renderer.idOf(e.target as Node)
        if (target === null) return
        if (HIGH_RATE_EVENTS.has(e.type)) {
          frame.set(e.type, { e, target })
          if (!frameScheduled) {
            frameScheduled = true
            requestAnimationFrame(() => {
              frameScheduled = false
              for (const { e: fe, target: ft } of frame.values()) send(fe, ft)
              frame.clear()
            })
          }
          return
        }
        send(e, target)
      }
      // a link with no listener of the page's still opens in a new tab
      if (!listened.has('click')) {
        listened.add('click')
        root.addEventListener('click', onEvent, { capture: true })
        cleanups.push(() => root.removeEventListener('click', onEvent, { capture: true }))
      }

      // ---- the Worker
      try {
        worker = new Worker(url, { name: `${pluginId}/${pageId}` })
      } catch (e) {
        fail(`The page could not start: ${(e as Error).message}`)
        return
      }
      workerRef.current = worker
      let pending = 0
      let started = false
      let second = 0
      let count = 0

      worker.onerror = e => {
        e.preventDefault()
        if (!started) fail(`The page could not start: ${e.message || 'its script did not load'}`)
        else setNotice(`The page threw: ${e.message || 'an error'}`)
      }
      worker.onmessageerror = () => setNotice('The page sent a message that could not be read.')
      worker.onmessage = (ev: MessageEvent) => {
        const now = Math.floor(Date.now() / 1000)
        if (now !== second) { second = now; count = 0 }
        if (++count > MAX_MESSAGES_PER_SECOND) {
          fail('The page was stopped: it sent too many messages.')
          return
        }
        const m = ev.data as Record<string, unknown> | null
        if (!m || typeof m !== 'object' || m.lucode !== 1) return
        switch (m.t) {
          case 'ready':
            started = true
            setStatus({ kind: 'running' })
            worker?.postMessage({ lucode: 1, t: 'event', name: 'theme', data: 'dark' })
            break
          case 'ops': {
            const err = renderer.apply(m.ops)
            if (err) fail(`The page was stopped: ${err}.`)
            break
          }
          case 'rpc': {
            const id = m.id
            const name = typeof m.name === 'string' ? m.name : ''
            const reply = (ok: boolean, payload: unknown) => worker?.postMessage(
              ok ? { lucode: 1, t: 'rpcres', id, ok: true, value: payload } : { lucode: 1, t: 'rpcres', id, ok: false, error: String(payload) })
            if (!allowed.has(name)) {
              reply(false, `this page cannot call "${name}": plugin.json does not list it in the page's "rpc"`)
              break
            }
            if (pending >= MAX_PENDING_RPCS) {
              reply(false, 'too many calls at once')
              break
            }
            const args = m.args && typeof m.args === 'object' && !Array.isArray(m.args) ? m.args as Record<string, unknown> : {}
            pending++
            api.callPluginRpc(pluginId, name, projectId, args)
              .then(v => reply(true, v), e => reply(false, pluginErrorMessage(e)))
              .finally(() => { pending-- })
            break
          }
          case 'toast':
            if (typeof m.message === 'string' && m.message.trim()) usePlugins.getState().toast(m.message.slice(0, 500), toastTone(m.tone), pluginId)
            break
          case 'open': {
            const link = typeof m.url === 'string' ? safeLink(m.url) : null
            if (link && Date.now() - lastUserEvent < 5000) window.open(link, '_blank', 'noopener,noreferrer')
            break
          }
          case 'evdone': {
            const href = typeof m.seq === 'number' ? links.get(m.seq) : undefined
            if (href === undefined) break
            links.delete(m.seq as number)
            const link = !m.prevented ? safeLink(href) : null
            if (link) window.open(link, '_blank', 'noopener,noreferrer')
            break
          }
          case 'error':
            if (typeof m.message === 'string') setNotice(`The page threw: ${m.message.slice(0, 500)}`)
            break
        }
      }
    }).catch(e => fail(pluginErrorMessage(e)))

    return () => {
      cancelled = true
      worker?.terminate()
      workerRef.current = null
      for (const c of cleanups) c()
      for (const u of assetUrls) URL.revokeObjectURL(u)
      host.shadowRoot?.replaceChildren()
    }
  }, [pluginId, pageId, projectId])

  // a webhook reached this plugin: the page decides what to do with it
  useEffect(() => {
    if (webhookTick > 0) workerRef.current?.postMessage({ lucode: 1, t: 'event', name: 'webhook', data: { at: Date.now() } })
  }, [webhookTick])

  return (
    <div className="plugin-surface-wrap">
      {notice && status.kind !== 'failed' && (
        <p className="plugin-surface__notice" role="status">
          <IconAlertTriangle size={14} aria-hidden /> {notice}
          <button className="plugin-icon-btn" onClick={() => setNotice('')} aria-label="Dismiss">×</button>
        </p>
      )}
      {status.kind === 'failed' && (
        <div className="plugin-page__failure" role="alert">
          <IconAlertTriangle size={22} stroke={1.6} aria-hidden />
          <p className="pv-empty__title">This page stopped</p>
          <p className="pv-empty__text">{status.message}</p>
        </div>
      )}
      <div
        ref={hostRef}
        className="plugin-surface"
        aria-busy={status.kind === 'loading'}
        hidden={status.kind === 'failed'}
      />
    </div>
  )
}
