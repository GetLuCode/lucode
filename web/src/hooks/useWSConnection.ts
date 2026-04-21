import ReconnectingWebSocket from 'reconnecting-websocket'

type MessageHandler = (event: MessageEvent) => void

// Global singleton WS connection
let rws: ReconnectingWebSocket | null = null
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
  return `${protocol}//localhost:8090/ws`
}

export async function initWSConnection(): Promise<ReconnectingWebSocket> {
  if (rws) return rws
  const url = await getWSUrl()
  console.log(`[ws] connecting to ${url}`)
  rws = new ReconnectingWebSocket(url, [], {
    maxRetries: Infinity,
    reconnectionDelayGrowFactor: 1.5,
    minReconnectionDelay: 1000,
    maxReconnectionDelay: 10000,
  })
  rws.onopen = () => console.log('[ws] connected')
  rws.onclose = () => console.log('[ws] disconnected, reconnecting...')
  rws.onerror = (err) => console.error('[ws] error:', err)
  rws.onmessage = (e: MessageEvent) => {
    for (const handler of handlers) handler(e)
  }
  return rws
}

export function getWSConnection(): ReconnectingWebSocket {
  if (rws) return rws

  // Fallback sync path (browser dev mode, port 8090)
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  const url = `${protocol}//localhost:8090/ws`
  console.log(`[ws] connecting to ${url}`)

  rws = new ReconnectingWebSocket(url, [], {
    maxRetries: Infinity,
    reconnectionDelayGrowFactor: 1.5,
    minReconnectionDelay: 1000,
    maxReconnectionDelay: 10000,
  })

  rws.onopen = () => {
    console.log('[ws] connected')
  }

  rws.onclose = () => {
    console.log('[ws] disconnected, reconnecting...')
  }

  rws.onerror = (err) => {
    console.error('[ws] error:', err)
  }

  rws.onmessage = (e: MessageEvent) => {
    for (const handler of handlers) {
      handler(e)
    }
  }

  return rws
}

export function addWSMessageHandler(handler: MessageHandler): () => void {
  handlers.add(handler)
  return () => handlers.delete(handler)
}

export function sendWSMessage(msg: unknown): void {
  const ws = getWSConnection()
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
