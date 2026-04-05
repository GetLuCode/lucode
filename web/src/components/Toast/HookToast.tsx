import { useState, useEffect } from 'react'
import { motion } from 'framer-motion'
import { ToolInputRenderer } from '../Hook/HookToolInput'
import type { PendingHook } from '../../types'

interface Props {
  hook: PendingHook
  onApprove: () => void
  onDeny: () => void
}

export function HookToast({ hook, onApprove, onDeny }: Props) {
  const [remaining, setRemaining] = useState(240) // 4 minutes in seconds
  const [isExpired, setIsExpired] = useState(false)

  useEffect(() => {
    const createdMs = new Date(hook.created_at).getTime()
    const deadline = createdMs + 4 * 60 * 1000

    const interval = setInterval(() => {
      const now = Date.now()
      const secondsLeft = Math.max(0, Math.floor((deadline - now) / 1000))
      setRemaining(secondsLeft)

      if (secondsLeft === 0) {
        setIsExpired(true)
        clearInterval(interval)
      }
    }, 1000)

    return () => clearInterval(interval)
  }, [hook.created_at])

  const progress = (remaining / 240) * 100
  const minutes = Math.floor(remaining / 60)
  const seconds = remaining % 60

  return (
    <motion.div
      className="hook-toast"
      initial={{ opacity: 0, y: -20, x: 20 }}
      animate={{ opacity: 1, y: 0, x: 0 }}
      exit={{ opacity: 0, y: -20, x: 20 }}
      transition={{ duration: 0.2 }}
    >
      <div className="hook-toast__header">
        <div className="hook-toast__icon">⚙</div>
        <div className="hook-toast__title-group">
          <div className="hook-toast__title">Tool Use Request</div>
          <div className="hook-toast__subtitle">
            <span className="hook-toast__tool-badge">{hook.tool_name}</span>
          </div>
        </div>
      </div>

      <div className="hook-toast__body">
        <ToolInputRenderer toolName={hook.tool_name} input={hook.tool_input} />
      </div>

      <div className="hook-toast__progress-container">
        <div className="hook-toast__progress-bar">
          <div className="hook-toast__progress-fill" style={{ width: `${progress}%` }} />
        </div>
        <div className="hook-toast__timer">
          {isExpired ? '⏱ Timed out' : `${minutes}:${String(seconds).padStart(2, '0')} remaining`}
        </div>
      </div>

      <div className="hook-toast__actions">
        <button
          className="btn btn--deny hook-toast__btn"
          onClick={onDeny}
          disabled={isExpired}
        >
          Deny
        </button>
        <button
          className="btn btn--approve hook-toast__btn"
          onClick={onApprove}
          disabled={isExpired}
        >
          Approve
        </button>
      </div>
    </motion.div>
  )
}
