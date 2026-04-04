import { motion, AnimatePresence } from 'framer-motion'
import type { PendingHook } from '../../types'

interface Props {
  hook: PendingHook | null
  onApprove: () => void
  onDeny: () => void
}

export function ToolApprovalModal({ hook, onApprove, onDeny }: Props) {
  return (
    <AnimatePresence>
      {hook && (
        <motion.div
          className="modal-overlay"
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
        >
          <motion.div
            className="modal"
            initial={{ opacity: 0, scale: 0.95, y: 12 }}
            animate={{ opacity: 1, scale: 1, y: 0 }}
            exit={{ opacity: 0, scale: 0.95, y: 12 }}
            transition={{ duration: 0.18, ease: 'easeOut' }}
          >
            <div className="modal__header">
              <div className="modal__icon">⚙</div>
              <div>
                <div className="modal__title">Tool Use Request</div>
                <div className="modal__subtitle">PreToolUse</div>
              </div>
            </div>

            <div className="modal__tool-name">
              <span className="modal__tool-badge">{hook.tool_name}</span>
            </div>

            <pre className="modal__code">
              {JSON.stringify(hook.tool_input, null, 2)}
            </pre>

            <div className="modal__actions">
              <button className="btn btn--deny" onClick={onDeny}>
                Deny
              </button>
              <button className="btn btn--approve" onClick={onApprove}>
                Approve
              </button>
            </div>
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>
  )
}
