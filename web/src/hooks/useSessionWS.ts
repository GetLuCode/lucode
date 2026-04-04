import { useEffect, useRef, useCallback } from 'react'
import { useStore } from '../store'
import type { WSEvent } from '../types'

const WS_BASE = 'ws://localhost:8090'

export function useSessionWS(sessionId: string | null) {
  const wsRef = useRef<WebSocket | null>(null)
  const retryRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const retryDelay = useRef(1000)
  const handleWSEvent = useStore(s => s.handleWSEvent)
  const respondHook = useStore(s => s.respondHook)
  const pendingHook = useStore(s => s.pendingHook)

  const connect = useCallback(() => {
    if (!sessionId) return

    const ws = new WebSocket(`${WS_BASE}/ws?role=client&session_id=${sessionId}`)
    wsRef.current = ws

    ws.onopen = () => {
      retryDelay.current = 1000
    }

    ws.onmessage = (e) => {
      try {
        const event: WSEvent = JSON.parse(e.data)
        handleWSEvent(event)
      } catch {}
    }

    ws.onclose = () => {
      wsRef.current = null
      // Reconnect with backoff
      retryRef.current = setTimeout(() => {
        retryDelay.current = Math.min(retryDelay.current * 2, 30000)
        connect()
      }, retryDelay.current)
    }

    ws.onerror = () => {
      ws.close()
    }
  }, [sessionId, handleWSEvent])

  // Expose send function for hook_respond
  const sendHookRespond = useCallback(
    (decision: 'approve' | 'deny', reason?: string) => {
      if (!sessionId) return
      const ws = wsRef.current
      if (ws?.readyState === WebSocket.OPEN) {
        ws.send(
          JSON.stringify({
            type: 'hook_respond',
            session_id: sessionId,
            decision,
            reason: reason ?? '',
          })
        )
      }
      respondHook(sessionId, decision, reason)
    },
    [sessionId, respondHook]
  )

  useEffect(() => {
    connect()
    return () => {
      if (retryRef.current) clearTimeout(retryRef.current)
      wsRef.current?.close()
      wsRef.current = null
    }
  }, [connect])

  return { sendHookRespond, pendingHook }
}
