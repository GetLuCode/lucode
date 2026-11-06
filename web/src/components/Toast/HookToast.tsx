import { useNavigate } from 'react-router-dom'
import { motion } from 'framer-motion'
import { HookHeader, HookProgress } from '../Hook/HookHeader'
import { ToolInputRenderer } from '../Hook/HookToolInput'
import { useHookCountdown } from '../Hook/useHookCountdown'
import type { PendingHook } from '../../types'

interface Props {
  hook: PendingHook
  projectId?: string
}

export function HookToast({ hook, projectId }: Props) {
  const remaining = useHookCountdown(hook.created_at)
  const navigate = useNavigate()

  const handleClick = () => {
    if (projectId) {
      navigate(`/projects/${projectId}/sessions/${hook.session_id}`)
    }
  }

  return (
    <motion.div
      className="hook-toast hook-toast--clickable"
      initial={{ opacity: 0, y: -20, x: 20 }}
      animate={{ opacity: 1, y: 0, x: 0 }}
      exit={{ opacity: 0, y: -20, x: 20 }}
      transition={{ duration: 0.2 }}
      onClick={handleClick}
      role="button"
      tabIndex={0}
      onKeyDown={e => e.key === 'Enter' && handleClick()}
    >
      <HookHeader toolName={hook.tool_name} remaining={remaining} />
      <div className="hook-toast__body">
        <ToolInputRenderer toolName={hook.tool_name} input={hook.tool_input} />
      </div>
      <HookProgress remaining={remaining} />
    </motion.div>
  )
}
