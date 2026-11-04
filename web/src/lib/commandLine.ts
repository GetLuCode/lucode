import type { ClaudeCommand } from '../types'

// One argument as a shell would need it: quoted when it has spaces or quotes.
function quoteArg(a: string): string {
  return /^[\w@%+=:,./\\-]+$/.test(a) ? a : `"${a.replace(/(["\\])/g, '\\$1').replace(/\r?\n/g, '\\n')}"`
}

// The command as one line, to copy it.
export function commandLine(c: Pick<ClaudeCommand, 'exe' | 'args'>): string {
  return [c.exe, ...c.args].map(quoteArg).join(' ')
}

// "--model=haiku" -> ["--model", "haiku"]; positional values keep an empty name.
export function splitArg(a: string): { flag: string; value: string } {
  const m = /^(--?[\w-]+)=([\s\S]*)$/.exec(a)
  return m ? { flag: m[1], value: m[2] } : { flag: a, value: '' }
}

// Short preview of a command for lists: the flags that say what the run was.
export function commandSummary(c: Pick<ClaudeCommand, 'args'>): string {
  const keep = ['--model', '--name', '--resume', '--session-id', '--worktree', '--permission-mode']
  return c.args
    .map(splitArg)
    .filter(a => keep.includes(a.flag))
    .map(a => (a.value ? `${a.flag}=${a.value.length > 24 ? a.value.slice(0, 8) + '…' : a.value}` : a.flag))
    .join(' ')
}
