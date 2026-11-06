import * as api from '../../api'
import type { GitTarget } from './gitTypes'

// Where a call goes: the project, the runner and directory, and optionally one linked worktree.
export interface GitCallContext {
  projectId: string
  target: GitTarget | null
  worktree: string // "" = the main checkout
}

export function gitCall<T>(ctx: GitCallContext, op: string, body: Record<string, unknown> = {}): Promise<T> {
  if (!ctx.target) return Promise.reject(new Error('No repository selected'))
  return api.projectGit<T>(ctx.projectId, op, {
    runner_id: ctx.target.runner_id,
    repo: ctx.target.repo,
    ...(ctx.worktree ? { worktree: ctx.worktree } : {}),
    ...body,
  })
}

// The server answers errors as "METHOD path → 409: text"; show just the text.
export function errorText(e: unknown): string {
  // A hub without these routes answers with the web page, and that is not JSON.
  if (e instanceof SyntaxError) return 'The hub is older than this page and does not know the Git tab yet: restart it.'
  const m = e instanceof Error ? e.message : String(e)
  return m.replace(/^.*?→ \d+: /, '').trim()
}

export const shortSha = (sha: string) => sha.slice(0, 7)

// A stable color for a person, from the same family as the graph's lanes.
export function authorHue(name: string): number {
  let h = 0
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) % 360
  return h
}

export function initials(name: string): string {
  const parts = name.trim().split(/[\s._-]+/).filter(Boolean)
  if (parts.length === 0) return '?'
  if (parts.length === 1) return parts[0].slice(0, 2).toUpperCase()
  return (parts[0][0] + parts[parts.length - 1][0]).toUpperCase()
}

const UNITS: [number, string][] = [[31536000, 'y'], [2592000, 'mo'], [604800, 'w'], [86400, 'd'], [3600, 'h'], [60, 'm']]

export function ago(unix: number, now = Date.now() / 1000): string {
  const s = Math.max(0, now - unix)
  for (const [size, unit] of UNITS) {
    if (s >= size) return `${Math.floor(s / size)}${unit}`
  }
  return 'now'
}

export const fullDate = (unix: number) => new Date(unix * 1000).toLocaleString()
