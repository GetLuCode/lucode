import { useEffect, useRef, useCallback } from 'react'
import { StringCodec } from 'nats.ws'
import type { Subscription } from 'nats.ws'
import { useStore } from '../store'
import { getNatsConnection } from './useNatsConnection'
import { logNatsMessage } from '../lib/natsDebug'
import type { WSEvent } from '../types'

const sc = StringCodec()

export function useSessionWS(sessionId: string | null) {
  const subRef = useRef<Subscription | null>(null)
  const handleWSEvent = useStore(s => s.handleWSEvent)
  const handleWSEventRef = useRef(handleWSEvent)
  handleWSEventRef.current = handleWSEvent

  useEffect(() => {
    if (!sessionId) {
      // Unsubscribe if session ID is cleared
      if (subRef.current) {
        subRef.current.unsubscribe()
        subRef.current = null
      }
      return
    }

    let destroyed = false

    ;(async () => {
      try {
        const nc = await getNatsConnection()
        if (destroyed) return

        const subj = `sess.${sessionId}.events`
        console.log(`[nats] subscribing to ${subj}`)

        // Unsubscribe from previous session if any
        if (subRef.current) {
          subRef.current.unsubscribe()
        }

        const sub = nc.subscribe(subj)
        subRef.current = sub

        // Drain subscription messages
        ;(async () => {
          for await (const msg of sub) {
            if (destroyed) break
            try {
              const text = sc.decode(msg.data)
              logNatsMessage('recv', msg.subject, msg.data)
              const event: WSEvent = JSON.parse(text)
              console.log(`[nats] parsed event type=${event.type} for sess=${sessionId}`)
              handleWSEventRef.current(event)
            } catch (err) {
              console.error(`[nats] decode error for subject=${msg.subject}:`, err, 'data:', msg.data)
            }
          }
        })()
      } catch (err) {
        console.error('[nats] subscription failed:', err)
      }
    })()

    return () => {
      destroyed = true
      if (subRef.current) {
        subRef.current.unsubscribe()
        subRef.current = null
      }
    }
  }, [sessionId])

  const sendHookRespond = useCallback(
    (toolUseId: string, decision: 'approve' | 'deny', reason?: string) => {
      if (!sessionId) return
      ;(async () => {
        try {
          const nc = await getNatsConnection()
          const payload = sc.encode(JSON.stringify({
            type: 'hook_respond',
            session_id: sessionId,
            tool_use_id: toolUseId,
            decision,
            reason: reason ?? '',
          }))
          nc.publish(`sess.${sessionId}.cmd`, payload)
        } catch (err) {
          console.error('[nats] publish failed:', err)
        }
      })()
    },
    [sessionId]
  )

  return { sendHookRespond }
}
