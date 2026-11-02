import { useCallback } from 'react'
import { sendWSMessage } from './useWSConnection'

// Incoming events are handled globally by useGlobalWS (mounted in App); this
// hook only exposes the approval reply for the session shown in the chat.
export function useSessionWS(sessionId: string | null) {
  const sendHookRespond = useCallback(
    (toolUseId: string, decision: 'approve' | 'deny' | 'allow_always' | 'allow_with_rule' | 'deny_always', reason?: string, rule?: { tool: string; input_regex?: Record<string, string> }) => {
      if (!sessionId) return
      sendWSMessage({
        type: 'hook_respond',
        session_id: sessionId,
        tool_use_id: toolUseId,
        decision,
        reason: reason ?? '',
        rule: rule ? JSON.stringify(rule) : undefined,
      })
    },
    [sessionId]
  )

  return { sendHookRespond }
}
