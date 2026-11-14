export interface TermStatus {
  id: string
  state: 'running' | 'exited' | 'failed'
  pid?: number
  exit_code?: number
  error?: string
  command: string
  cwd: string
  started_at?: string
  ended_at?: string
}

export interface ProjectCommand {
  id: string
  project_id: string
  name: string
  command: string // empty = an interactive shell
  cwd: string // relative to the project's work dir on the runner
  runner_id: string // "" = the project's runner
  runner: string // the runner it actually runs on
  status?: TermStatus
}

export interface CommandTarget { runner_id: string; hostname: string; os: string; online: boolean; repo: string }

export interface CommandList { commands: ProjectCommand[]; targets: CommandTarget[]; reason?: string }

// A path that stays inside the work dir, as the hub checks it too.
export function relDirProblem(rel: string): string {
  const r = rel.trim()
  if (!r) return ''
  if (/^([a-zA-Z]:|[\\/])/.test(r)) return 'Use a folder relative to the work directory'
  if (r.split(/[\\/]/).includes('..')) return 'The folder must stay inside the work directory'
  return ''
}

export function stateLabel(s?: TermStatus): string {
  if (!s) return 'Not started'
  if (s.state === 'running') return 'Running'
  if (s.state === 'failed') return s.error ? `Failed: ${s.error}` : 'Failed to start'
  return s.exit_code === 0 ? 'Finished' : `Exited with code ${s.exit_code ?? '?'}`
}
