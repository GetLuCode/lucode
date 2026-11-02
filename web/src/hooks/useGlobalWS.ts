import { useEffect, useRef } from 'react'
import { useStore } from '../store'
import { getWSConnection, addWSMessageHandler } from './useWSConnection'
import type { WSEvent } from '../types'

// Feeds every WebSocket event, from every session, into the store.
//
// Tasks run in the background on their agent, so status changes and approval
// requests arrive for sessions the user is not looking at (or while a full-page
// view such as the kanban board is open instead of the chat). A per-chat handler
// would drop them, so this one is mounted once at the app root.
export function useGlobalWS() {
  const handleWSEvent = useStore(s => s.handleWSEvent)
  const increaseSessionMessageCount = useStore(s => s.increaseSessionMessageCount)
  const handleRef = useRef(handleWSEvent)
  const increaseRef = useRef(increaseSessionMessageCount)
  handleRef.current = handleWSEvent
  increaseRef.current = increaseSessionMessageCount

  useEffect(() => {
    const wsOrPromise = getWSConnection()
    if (wsOrPromise instanceof Promise) {
      wsOrPromise.catch(err => console.error('[ws] init error:', err))
    }

    return addWSMessageHandler((e: MessageEvent) => {
      try {
        const event = JSON.parse(e.data) as WSEvent & { session_id?: string }
        handleRef.current(event)
        // Count complete messages only, not streaming chunks
        if (event.session_id && (event.type === 'user_message' || event.type === 'tool_execution')) {
          increaseRef.current(event.session_id)
        }
      } catch (err) {
        console.error('[ws] parse error:', err, 'raw:', e.data)
      }
    })
  }, [])
}
