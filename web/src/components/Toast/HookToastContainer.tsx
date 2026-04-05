import { AnimatePresence } from 'framer-motion'
import { useStore } from '../../store'
import { HookToast } from './HookToast'

export function HookToastContainer() {
  const pendingHooks = useStore(s => s.pendingHooks)
  const activeProjectId = useStore(s => s.activeProjectId)
  const activeSessionId = useStore(s => s.activeSessionId)

  // Flatten all pending hooks from all sessions, except the active one
  const hooksBySession = Object.entries(pendingHooks).filter(([sessionId]) => sessionId !== activeSessionId)

  return (
    <div className="hook-toast-container">
      <AnimatePresence mode="popLayout">
        {hooksBySession.map(([sessionId, hooks]) => (
          <div key={sessionId}>
            {hooks.map(hook => (
              <HookToast
                key={`${hook.session_id}-${hook.tool_use_id}`}
                hook={hook}
                projectId={activeProjectId || undefined}
              />
            ))}
          </div>
        ))}
      </AnimatePresence>
    </div>
  )
}
