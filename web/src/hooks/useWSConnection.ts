import ReconnectingWebSocket from 'reconnecting-websocket'

type MessageHandler = (event: MessageEvent) => void

// Global singleton WS connection
let rws: ReconnectingWebSocket | null = null
const handlers = new Set<MessageHandler>()

function getWSUrl(): string {
  const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${protocol}//localhost:8090/ws`
}

export function getWSConnection(): ReconnectingWebSocket {
  if (rws) return rws

  const url = getWSUrl()
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
