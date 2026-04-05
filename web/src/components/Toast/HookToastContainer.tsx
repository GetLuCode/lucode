import { AnimatePresence } from 'framer-motion'
import { useStore } from '../../store'
import { HookToast } from './HookToast'

export function HookToastContainer() {
  const pendingHooks = useStore(s => s.pendingHooks)
  const respondHook = useStore(s => s.respondHook)

  // Flatten all pending hooks from all sessions
  const allPending = Object.values(pendingHooks).flat()

  return (
    <div className="hook-toast-container">
      <AnimatePresence mode="popLayout">
        {allPending.map(hook => (
          <HookToast
            key={`${hook.session_id}-${hook.tool_use_id}`}
            hook={hook}
            onApprove={() => respondHook(hook.session_id, 'approve')}
            onDeny={() => respondHook(hook.session_id, 'deny')}
          />
        ))}
      </AnimatePresence>
    </div>
  )
}
