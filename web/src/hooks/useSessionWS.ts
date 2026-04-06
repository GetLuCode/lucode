import { useEffect, useRef, useCallback } from 'react'
import { useStore } from '../store'
import type { WSEvent } from '../types'

const WS_BASE = `${window.location.protocol === 'https:' ? 'wss:' : 'ws:'}//${window.location.host}`

export function useSessionWS(sessionId: string | null) {
  const wsRef = useRef<WebSocket | null>(null)
  const handleWSEvent = useStore(s => s.handleWSEvent)
  const handleWSEventRef = useRef(handleWSEvent)
  handleWSEventRef.current = handleWSEvent

  useEffect(() => {
    if (!sessionId) return

    let destroyed = false
    let retryDelay = 1000
    let retryTimeout: ReturnType<typeof setTimeout> | null = null

    function connect() {
      if (destroyed) return

      const ws = new WebSocket(`${WS_BASE}/ws?role=client&session_id=${sessionId}`)
      wsRef.current = ws

      ws.onopen = () => { retryDelay = 1000 }

      ws.onmessage = (e) => {
        try {
          const event: WSEvent = JSON.parse(e.data)
          handleWSEventRef.current(event)
        } catch {}
      }

      ws.onclose = () => {
        wsRef.current = null
        if (!destroyed) {
          retryTimeout = setTimeout(() => {
            retryDelay = Math.min(retryDelay * 2, 30000)
            connect()
          }, retryDelay)
        }
      }

      ws.onerror = () => { ws.close() }
    }

    connect()

    return () => {
      destroyed = true
      if (retryTimeout) clearTimeout(retryTimeout)
      wsRef.current?.close()
      wsRef.current = null
    }
  }, [sessionId])

  const sendHookRespond = useCallback(
    (toolUseId: string, decision: 'approve' | 'deny', reason?: string) => {
      if (!sessionId) return
      const ws = wsRef.current
      if (ws?.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({
          type: 'hook_respond',
          session_id: sessionId,
          tool_use_id: toolUseId,
          decision,
          reason: reason ?? '',
        }))
      }
    },
    [sessionId]
  )

  return { sendHookRespond }
}
