// What an approval request says about itself: plain logic, no React, so it can be tested.

/** The server gives up on an unanswered approval after this long (session.Manager, lucodehook). */
export const HOOK_TIMEOUT_SECONDS = 240

const ACTIONS: Record<string, string> = {
  Bash: 'Run a command',
  Write: 'Write a file',
  Edit: 'Edit a file',
  MultilineEdit: 'Edit a file',
  Read: 'Read a file',
  Glob: 'Find files',
  Grep: 'Search files',
  LS: 'List a folder',
  Skill: 'Activate a skill',
}

/** What the agent wants to do, in words. */
export function hookAction(toolName: string): string {
  return ACTIONS[toolName] ?? 'Use a tool'
}

export type HookOutcome = 'allowed' | 'denied' | 'timeout' | 'unknown'

/** How an answered approval reads in the chat history. The server stores the UI's decision as is. */
export function hookOutcome(decision: string): { label: string; outcome: HookOutcome } {
  switch (decision) {
    case 'approve': return { label: 'Approved', outcome: 'allowed' }
    case 'allow_always': return { label: 'Allowed always', outcome: 'allowed' }
    case 'allow_with_rule': return { label: 'Allowed by rule', outcome: 'allowed' }
    case 'deny': return { label: 'Denied', outcome: 'denied' }
    case 'deny_always': return { label: 'Denied always', outcome: 'denied' }
    case 'timeout': return { label: 'Timed out', outcome: 'timeout' }
    default: return { label: decision || 'Answered', outcome: 'unknown' }
  }
}

/** One line that identifies the request when it is collapsed in the history. */
export function hookSummary(toolName: string, input: unknown): string {
  const data = (input && typeof input === 'object' ? input : {}) as Record<string, unknown>
  const text = (v: unknown) => (typeof v === 'string' ? v : '')
  const firstLine = (s: string) => s.split(/\r?\n/).find(l => l.trim() !== '')?.trim() ?? ''

  switch (toolName) {
    case 'Bash':
      return firstLine(text(data.command))
    case 'Skill':
      return text(data.skill)
    case 'Grep':
      return firstLine(text(data.pattern))
    case 'Glob':
      return text(data.pattern)
    default: {
      const path = text(data.file_path) || text(data.path)
      if (path) return path
      const firstString = Object.values(data).find(v => typeof v === 'string' && v.trim() !== '')
      return firstLine(text(firstString))
    }
  }
}

/** Splits a Unix or Windows path into its folder (with the trailing separator) and its file. */
export function splitPath(path: string): { dir: string; file: string } {
  const cut = Math.max(path.lastIndexOf('/'), path.lastIndexOf('\\')) + 1
  return { dir: path.slice(0, cut), file: path.slice(cut) }
}

/** Seconds until the server gives up. A missing or unreadable date counts as just created. */
export function hookSecondsLeft(createdAt: string | undefined, now: number): number {
  const created = new Date(createdAt ?? '').getTime()
  if (Number.isNaN(created)) return HOOK_TIMEOUT_SECONDS
  const left = Math.floor((created + HOOK_TIMEOUT_SECONDS * 1000 - now) / 1000)
  return Math.min(HOOK_TIMEOUT_SECONDS, Math.max(0, left))
}

export function formatCountdown(seconds: number): string {
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`
}
