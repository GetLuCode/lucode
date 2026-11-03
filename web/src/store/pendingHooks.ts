import type { Message, PendingHook } from '../types'

// An approval waiting for the user is shown in the chat as a virtual "hook"
// message. It only exists in memory: the server stores the hook message when it is
// answered, so a session's messages loaded from the API never contain it.
// Anything that rebuilds a session's messages must therefore put it back, or the
// approval disappears from the chat (it stays in pendingHooks, so the toast still
// shows) until the page is reloaded and the server replays it.

/** The virtual chat message for an approval that is waiting for an answer. */
export function pendingHookMessage(hook: PendingHook, seq: number): Message {
  return {
    id: `hook-${hook.tool_use_id}`,
    session_id: hook.session_id,
    role: 'hook',
    content: JSON.stringify({
      tool_use_id: hook.tool_use_id,
      tool_name: hook.tool_name,
      tool_input: hook.tool_input,
      tool_input_schema: hook.tool_input_schema ?? null,
      decision: 'pending',
    }),
    seq,
    created_at: hook.created_at || new Date().toISOString(),
  }
}

/**
 * Returns messages with a virtual message for every pending hook that is not
 * already in the list. Returns the same array when nothing is missing, so callers
 * can tell that nothing changed.
 *
 * A hook counts as present when a message has its virtual id, or when a stored
 * "hook" message already carries its tool_use_id (the answer was just persisted).
 */
export function withPendingHooks(messages: Message[], hooks: PendingHook[]): Message[] {
  const missing = hooks.filter(h =>
    !messages.some(m =>
      m.id === `hook-${h.tool_use_id}` || (m.role === 'hook' && m.content.includes(h.tool_use_id)),
    ),
  )
  if (missing.length === 0) return messages
  // A pending approval is always the latest thing in a session: the turn is waiting on it.
  return [...messages, ...missing.map((h, i) => pendingHookMessage(h, messages.length + i))]
}
