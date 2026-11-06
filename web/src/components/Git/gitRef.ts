// A reference to a file or to some lines of a diff: what gets copied, and the block the Supervisor
// receives. Pure, for the tests. The block carries the selected lines themselves, so it says
// everything even where no tool can run (Gemini mode) or the repository is on another runner, and
// the arguments of git_diff for whoever wants to see more around them.

import type { DiffLine } from './gitDiff'
import type { DiffRequest, GitTarget } from './gitTypes'

const shortSha = (sha: string) => sha.slice(0, 7)

const MAX_LINES = 200
const MAX_CHARS = 12000

export interface LineRange {
  start: number
  end: number
  removed: boolean // only removed lines: the numbers are of the old version
}

// The numbers of a selection: the new version's when it has any, since that is where they can be
// looked at; only a selection of removed lines speaks of the old one.
export function rangeOf(lines: DiffLine[]): LineRange | null {
  const fresh = lines.flatMap(l => (l.newNo !== undefined ? [l.newNo] : []))
  const old = lines.flatMap(l => (l.oldNo !== undefined ? [l.oldNo] : []))
  const nums = fresh.length ? fresh : old
  if (nums.length === 0) return null
  return { start: Math.min(...nums), end: Math.max(...nums), removed: fresh.length === 0 }
}

export function sourceOf(r: DiffRequest): string {
  switch (r.mode) {
    case 'commit': return `commit ${shortSha(r.sha)}`
    case 'range': return `${r.base}...${r.head}`
    case 'stash': return r.sha
    case 'staged': return 'staged changes'
    case 'untracked': return 'new file, not committed'
    default: return 'working tree'
  }
}

// What gets pasted somewhere: path:lines, and where it comes from unless it is what is on disk.
export function referenceLabel(r: DiffRequest, range: LineRange | null): string {
  const lines = range ? `:${range.start}${range.end !== range.start ? `-${range.end}` : ''}` : ''
  const at = r.mode === 'commit' ? ` @ ${shortSha(r.sha)}`
    : r.mode === 'range' ? ` @ ${r.base}...${r.head}`
    : r.mode === 'stash' ? ` @ ${r.sha}`
    : r.mode === 'staged' ? ' (staged)'
    : ''
  return `${r.path}${lines}${at}${range?.removed ? ' (removed lines)' : ''}`
}

// The text of the lines. A mix of added and removed lines only makes sense as a diff; anything
// else is plain code, ready to paste.
export function codeOf(lines: DiffLine[]): string {
  const code = lines.filter(l => l.kind === 'add' || l.kind === 'del' || l.kind === 'ctx')
  const mixed = code.some(l => l.kind === 'add') && code.some(l => l.kind === 'del')
  return code.map(l => (mixed ? `${l.kind === 'add' ? '+' : l.kind === 'del' ? '-' : ' '}${l.text}` : l.text)).join('\n')
}

function asDiff(lines: DiffLine[]): { text: string; omitted: number } {
  const out: string[] = []
  let chars = 0
  let omitted = 0
  for (const l of lines) {
    const row = l.kind === 'add' ? `+${l.text}` : l.kind === 'del' ? `-${l.text}` : l.kind === 'ctx' ? ` ${l.text}` : l.kind === 'hunk' ? `@@ ${l.text}` : ''
    if (!row) continue
    if (out.length >= MAX_LINES || chars + row.length > MAX_CHARS) { omitted++; continue }
    out.push(row)
    chars += row.length + 1
  }
  return { text: out.join('\n'), omitted }
}

// Code can hold a fence itself: the block's own has to be longer than any inside.
function fence(text: string): string {
  const longest = Math.max(2, ...(text.match(/`{3,}/g) ?? []).map(m => m.length))
  return '`'.repeat(longest + 1)
}

export interface ChatReferenceDraft {
  label: string
  detail: string
  text: string
}

export function buildReference(r: DiffRequest, lines: DiffLine[], target: GitTarget | null, worktree: string): ChatReferenceDraft {
  const range = rangeOf(lines)
  const label = referenceLabel(r, range)
  const where = target ? `${target.repo} on ${target.hostname || target.runner_id}` : ''
  const args = {
    mode: r.mode,
    ...('sha' in r ? { sha: r.sha } : {}),
    ...('base' in r ? { base: r.base, head: r.head } : {}),
    path: r.path,
    ...('orig_path' in r && r.orig_path ? { orig_path: r.orig_path } : {}),
    ...('untracked' in r && r.untracked ? { untracked: true } : {}),
    ...(target ? { runner_id: target.runner_id, repo: target.repo } : {}),
    ...(worktree ? { worktree } : {}),
  }
  const parts = [
    `Git reference: ${label}`,
    `Source: ${sourceOf(r)}${where ? `, in ${where}` : ''}${worktree ? `, worktree ${worktree}` : ''}`,
    `More with git_diff: ${JSON.stringify(args)}`,
  ]
  const { text, omitted } = asDiff(lines)
  if (text) {
    const f = fence(text)
    parts.push(`${f}diff\n${text}\n${f}`)
    if (omitted) parts.push(`(${omitted} more selected lines left out: read them with git_diff.)`)
  }
  return { label, detail: `${sourceOf(r)}${where ? ` · ${where}` : ''}`, text: parts.join('\n') }
}
