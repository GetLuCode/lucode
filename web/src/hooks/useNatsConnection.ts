import { useEffect, useRef } from 'react'
import { connect } from 'nats.ws'
import type { NatsConnection } from 'nats.ws'
import { logNatsMessage } from '../lib/natsDebug'

// Global NATS connection singleton (created once, reused across all sessions)
let globalNatsConnection: NatsConnection | null = null
let connectionPromise: Promise<NatsConnection> | null = null
let failureCount = 0
const MAX_FAILURES = 3

async function getNatsConnection(): Promise<NatsConnection> {
  if (globalNatsConnection) return globalNatsConnection

  // If we're already connecting, return the pending promise
  if (connectionPromise) return connectionPromise

  connectionPromise = (async () => {
    try {
      const port = await getNatsWSPort()
      const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:'
      const host = window.location.hostname
      const url = `${proto}//${host}:${port}`

      console.log(`[nats] connecting to ${url}`)
      const nc = await connect({ servers: url })

      // Setup reconnect listener
      ;(async () => {
        await nc.closed()
        console.log('[nats] connection closed, clearing singleton')
        globalNatsConnection = null
        connectionPromise = null
        failureCount++
        if (failureCount < MAX_FAILURES) {
          // Auto-retry on next request
        }
      })()

      failureCount = 0
      globalNatsConnection = nc

      // Wrap for debug logging in dev mode
      const originalPublish = nc.publish.bind(nc)
      nc.publish = (subj: string, data: Uint8Array) => {
        logNatsMessage('send', subj, data)
        return originalPublish(subj, data)
      }

      console.log('[nats] connected')
      return nc
    } catch (err) {
      connectionPromise = null
      failureCount++
      throw err
    }
  })()

  return connectionPromise
}

async function getNatsWSPort(): Promise<number> {
  try {
    const res = await fetch('/api/config')
    const data = await res.json()
    if (data.nats_ws_port) return data.nats_ws_port
  } catch {}
  return 5224 // default fallback
}

// Hook to ensure NATS connection is established
export function useNatsConnection() {
  const ncRef = useRef<NatsConnection | null>(null)

  useEffect(() => {
    let mounted = true
    getNatsConnection()
      .then(nc => {
        if (mounted) ncRef.current = nc
      })
      .catch(err => {
        console.error('[nats] connection failed:', err)
      })

    return () => {
      mounted = false
    }
  }, [])

  return ncRef
}

// Export for external use
export { getNatsConnection }
