import { useEffect, useRef, useCallback } from 'react'
import { useStore } from '../store'
import { getWSConnection, addWSMessageHandler, sendWSMessage } from './useWSConnection'
import type { WSEvent } from '../types'

export function useSessionWS(sessionId: string | null) {
  const handleWSEvent = useStore(s => s.handleWSEvent)
  const increaseSessionMessageCount = useStore(s => s.increaseSessionMessageCount)
  const handleWSEventRef = useRef(handleWSEvent)
  const increaseCountRef = useRef(increaseSessionMessageCount)
  handleWSEventRef.current = handleWSEvent
  increaseCountRef.current = increaseSessionMessageCount

  useEffect(() => {
    // Ensure WS connection is established
    const wsOrPromise = getWSConnection()
    if (wsOrPromise instanceof Promise) {
      wsOrPromise.catch(err => console.error('[ws] init error:', err))
    }

    if (!sessionId) return

    const remove = addWSMessageHandler((e: MessageEvent) => {
      try {
        const raw = JSON.parse(e.data) as { type: string; session_id?: string }
        // Filter events for this session
        if (raw.session_id && raw.session_id !== sessionId) return
        const event = raw as unknown as WSEvent
        console.log(`[ws] event type=${event.type} sess=${sessionId}`)
        handleWSEventRef.current(event)
        // Increment count only for complete messages, not streaming chunks
        if (event.type === 'user_message' || event.type === 'tool_execution') {
          increaseCountRef.current(sessionId)
        }
      } catch (err) {
        console.error('[ws] parse error:', err, 'raw:', e.data)
      }
    })

    return remove
  }, [sessionId])

  const sendHookRespond = useCallback(
    (toolUseId: string, decision: 'approve' | 'deny', reason?: string) => {
      if (!sessionId) return
      sendWSMessage({
        type: 'hook_respond',
        session_id: sessionId,
        tool_use_id: toolUseId,
        decision,
        reason: reason ?? '',
      })
    },
    [sessionId]
  )

  return { sendHookRespond }
}
