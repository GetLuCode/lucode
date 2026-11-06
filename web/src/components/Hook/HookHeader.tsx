// The heading shared by the approval card in the chat and the approval toast.
import {
  IconEye,
  IconFilePencil,
  IconFilePlus,
  IconFolderOpen,
  IconSearch,
  IconSparkles,
  IconTerminal2,
  IconTool,
  type Icon,
} from '@tabler/icons-react'
import { HOOK_TIMEOUT_SECONDS, formatCountdown, hookAction } from './hookSummary'

const TOOL_ICONS: Record<string, Icon> = {
  Bash: IconTerminal2,
  Write: IconFilePlus,
  Edit: IconFilePencil,
  MultilineEdit: IconFilePencil,
  Read: IconEye,
  Glob: IconSearch,
  Grep: IconSearch,
  LS: IconFolderOpen,
  Skill: IconSparkles,
}

/** The last minute is when the countdown has to catch the eye. */
const URGENT_SECONDS = 60

export function HookHeader({ toolName, remaining }: { toolName: string; remaining: number }) {
  const ToolIcon = TOOL_ICONS[toolName] ?? IconTool
  const urgent = remaining <= URGENT_SECONDS

  return (
    <div className="hook-head">
      <ToolIcon size={16} stroke={1.75} className={`hook-head__icon${toolName === 'Skill' ? ' hook-head__icon--skill' : ''}`} aria-hidden="true" />
      <span className="hook-head__action">{hookAction(toolName)}</span>
      <span className="hook-head__tool" title={toolName}>{toolName}</span>
      <span
        className={`hook-head__timer${urgent ? ' hook-head__timer--urgent' : ''}`}
        title="Time left to answer. After that the request is denied."
        role="timer"
      >
        {remaining > 0 ? formatCountdown(remaining) : 'Timed out'}
      </span>
    </div>
  )
}

/** Thin bar with the time left, for the toast. */
export function HookProgress({ remaining }: { remaining: number }) {
  const urgent = remaining <= URGENT_SECONDS
  return (
    <div className="hook-progress" aria-hidden="true">
      <div
        className={`hook-progress__fill${urgent ? ' hook-progress__fill--urgent' : ''}`}
        style={{ width: `${(remaining / HOOK_TIMEOUT_SECONDS) * 100}%` }}
      />
    </div>
  )
}
