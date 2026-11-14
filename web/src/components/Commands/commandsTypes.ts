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

export interface EnvVar { name: string; value: string }

// A shell the runner offers: the ones it found (builtin) and the ones registered on it.
export interface RunnerShell { id: string; name: string; path: string; args: string[]; builtin: boolean }

// What the hub also checks when saving: names are letters, digits and _, not starting with a digit, and unique.
export function envProblem(env: EnvVar[]): string {
  const seen = new Set<string>()
  for (const v of env) {
    const name = v.name.trim()
    if (!name && !v.value) continue
    if (!name) return 'A variable has a value but no name'
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) return `"${name}" is not a valid name: letters, digits and _, not starting with a digit`
    if (seen.has(name.toUpperCase())) return `${name} is set twice`
    seen.add(name.toUpperCase())
  }
  return ''
}

export interface ProjectCommand {
  id: string
  project_id: string
  name: string
  command: string // empty = an interactive shell
  cwd: string // relative to the project's work dir on the runner
  runner_id: string // "" = the project's runner
  shell: string // id of one of the runner's shells; "" = its default
  env: EnvVar[] // added to the runner's environment when it starts
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
