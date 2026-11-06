import { useState } from 'react'
import { IconCheck, IconCopy } from '@tabler/icons-react'
import type { ClaudeCommand } from '../../types'
import { commandLine, splitArg } from '../../lib/commandLine'
import { copyText } from '../../lib/copyText'

const LONG = 240 // values longer than this (the system prompt) start collapsed

function ArgRow({ arg }: { arg: string }) {
  const { flag, value } = splitArg(arg)
  const [open, setOpen] = useState(false)
  const long = value.length > LONG
  return (
    <div className="cmd__arg">
      <span className="cmd__flag">{flag}</span>
      {value && (
        <span className="cmd__value">
          {long && !open ? value.slice(0, LONG) + '…' : value}
          {long && (
            <button className="cmd__more" onClick={() => setOpen(v => !v)}>
              {open ? 'less' : `all ${value.length} chars`}
            </button>
          )}
        </span>
      )}
    </div>
  )
}

// A claude command line a runner launched: where, as which process, and every argument.
export function CommandDetails({ cmd, runnerName }: { cmd: ClaudeCommand; runnerName?: string }) {
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    await copyText(commandLine(cmd))
    setCopied(true)
    setTimeout(() => setCopied(false), 900)
  }
  const at = new Date(cmd.at)
  return (
    <div className="cmd">
      <dl className="cmd__meta">
        {runnerName && (<><dt>Runner</dt><dd>{runnerName}</dd></>)}
        <dt>Started</dt><dd>{isNaN(at.getTime()) ? cmd.at : at.toLocaleString()}</dd>
        <dt>Directory</dt><dd className="cmd__mono">{cmd.cwd || '—'}</dd>
        <dt>Process</dt><dd>{cmd.pid > 0 ? `pid ${cmd.pid}` : 'did not start'}</dd>
      </dl>
      {cmd.error && <div className="cmd__error">{cmd.error}</div>}
      <div className="cmd__head">
        <span className="cmd__exe cmd__mono" title={cmd.exe}>{cmd.exe}</span>
        <button className="cmd__copy" onClick={copy} title="Copy the command line">
          {copied ? <IconCheck size={13} /> : <IconCopy size={13} />}
          <span>{copied ? 'Copied' : 'Copy'}</span>
        </button>
      </div>
      <div className="cmd__args">
        {cmd.args.map((a, i) => <ArgRow key={i} arg={a} />)}
      </div>
    </div>
  )
}
