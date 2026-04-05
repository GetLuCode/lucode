import { AnimatePresence } from 'framer-motion'
import { useStore } from '../../store'
import { useSessionWS } from '../../hooks/useSessionWS'
import { HookToast } from './HookToast'

export function HookToastContainer() {
  const pendingHooks = useStore(s => s.pendingHooks)

  // Flatten all pending hooks from all sessions to group by session
  const hooksBySession = Object.entries(pendingHooks)

  return (
    <div className="hook-toast-container">
      <AnimatePresence mode="popLayout">
        {hooksBySession.map(([sessionId, hooks]) => (
          <SessionHooks key={sessionId} sessionId={sessionId} hooks={hooks} />
        ))}
      </AnimatePresence>
    </div>
  )
}

function SessionHooks({ sessionId, hooks }: { sessionId: string; hooks: any[] }) {
  const { sendHookRespond } = useSessionWS(sessionId)

  return (
    <>
      {hooks.map(hook => (
        <HookToast
          key={`${hook.session_id}-${hook.tool_use_id}`}
          hook={hook}
          onApprove={() => sendHookRespond(hook.tool_use_id, 'approve')}
          onDeny={() => sendHookRespond(hook.tool_use_id, 'deny')}
        />
      ))}
    </>
  )
}
