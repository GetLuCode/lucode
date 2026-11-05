import ReconnectingWebSocket from 'reconnecting-websocket'

type MessageHandler = (event: MessageEvent) => void

// Global singleton WS connection and initialization promise
let rws: ReconnectingWebSocket | null = null
let initPromise: Promise<ReconnectingWebSocket> | null = null
const handlers = new Set<MessageHandler>()

async function getWSUrl(): Promise<string> {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  try {
    const wailsApp = (window as any).go?.main?.App
    if (wailsApp) {
      const port: number = await wailsApp.GetLucodePort()
      return `${protocol}//localhost:${port}/ws`
    }
  } catch {}
  // Same origin as the page: the hub serves /ws itself, and `pnpm dev` (Parcel) proxies it to :8090.
  // A hardcoded localhost would point a phone at the phone itself.
  return `${protocol}//${window.location.host}/ws`
}

// WebSocket of the voice-input bridge (same host as the main socket).
export async function getSttWSUrl(): Promise<string> {
  return (await getWSUrl()).replace(/\/ws$/, '/ws/stt')
}

function setupWSHandlers(ws: ReconnectingWebSocket): void {
  ws.onopen = () => console.log('[ws] connected')
  ws.onclose = () => console.log('[ws] disconnected, reconnecting...')
  ws.onerror = (err) => console.error('[ws] error:', err)
  ws.onmessage = (e: MessageEvent) => {
    for (const handler of handlers) handler(e)
  }
}

// Unified function to get or create the WS connection
// Handles both async initialization and sync access
export function getWSConnection(): ReconnectingWebSocket | Promise<ReconnectingWebSocket> {
  if (rws) return rws

  // If init is already in progress, return the promise to avoid creating duplicate connections
  if (initPromise) return initPromise

  // Start initialization (async path for Wails port resolution)
  initPromise = (async () => {
    const url = await getWSUrl()
    console.log(`[ws] connecting to ${url}`)

    rws = new ReconnectingWebSocket(url, [], {
      maxRetries: Infinity,
      reconnectionDelayGrowFactor: 1.5,
      minReconnectionDelay: 1000,
      maxReconnectionDelay: 10000,
    })

    setupWSHandlers(rws)
    initPromise = null
    return rws
  })()

  return initPromise
}

// Explicit async initialization (for App.tsx)
export async function initWSConnection(): Promise<ReconnectingWebSocket> {
  const result = getWSConnection()
  if (result instanceof Promise) {
    return result
  }
  return result
}

export function addWSMessageHandler(handler: MessageHandler): () => void {
  handlers.add(handler)
  return () => handlers.delete(handler)
}

export function sendWSMessage(msg: unknown): void {
  const wsOrPromise = getWSConnection()
  const send = (ws: ReconnectingWebSocket) => {
    if (ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(msg))
    } else {
      const onOpen = () => {
        ws.send(JSON.stringify(msg))
        ws.removeEventListener('open', onOpen)
      }
      ws.addEventListener('open', onOpen)
    }
  }

  if (wsOrPromise instanceof Promise) {
    wsOrPromise.then(send).catch(err => console.error('[ws] send error:', err))
  } else {
    send(wsOrPromise)
  }
}
