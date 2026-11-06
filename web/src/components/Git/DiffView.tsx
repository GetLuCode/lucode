import { useEffect, useMemo, useState } from 'react'
import { IconColumns1, IconColumns2 } from '@tabler/icons-react'
import { highlightCode } from '../../lib/highlight'
import { gitCall, errorText } from './gitApi'
import type { GitCallContext } from './gitApi'
import type { DiffRequest, GitDiffAnswer } from './gitTypes'
import { languageOf, parseDiff, toSplitRows } from './gitDiff'
import { Stat } from './gitParts'
import type { DiffLine } from './gitDiff'

const MAX_HIGHLIGHTED_LINES = 4000

function loadSplit() {
  try { return localStorage.getItem('lucode-git-diff-split') === '1' } catch { return false }
}

function Code({ line, lang, plain }: { line: DiffLine; lang: string; plain: boolean }) {
  // highlightCode escapes what it does not wrap, so this HTML is safe to set
  const html = useMemo(() => (plain ? '' : highlightCode(line.text, lang).html), [line.text, lang, plain])
  if (plain) return <code className="gd-code">{line.text || ' '}</code>
  return <code className="gd-code" dangerouslySetInnerHTML={{ __html: html || ' ' }} />
}

const SIGN: Record<string, string> = { add: '+', del: '-', ctx: ' ' }

// One file's diff. Loads what it is asked for and shows it unified or side by side.
export function DiffView({ ctx, request, version }: { ctx: GitCallContext; request: DiffRequest | null; version?: string }) {
  const [answer, setAnswer] = useState<GitDiffAnswer | null>(null)
  const [error, setError] = useState('')
  const [split, setSplit] = useState(loadSplit)
  const key = JSON.stringify([request, ctx.target?.repo, ctx.worktree, version])

  useEffect(() => {
    if (!request) return
    let cancelled = false
    setError('')
    setAnswer(prev => (prev && version !== undefined ? prev : null))
    gitCall<GitDiffAnswer>(ctx, 'diff', { ...request })
      .then(a => { if (!cancelled) setAnswer(a) })
      .catch(e => { if (!cancelled) { setError(errorText(e)); setAnswer(null) } })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  const parsed = useMemo(() => (answer ? parseDiff(answer.text) : null), [answer])
  const lang = request ? languageOf(request.path) : ''
  const plain = (parsed?.lines.length ?? 0) > MAX_HIGHLIGHTED_LINES
  const rows = useMemo(() => (split && parsed ? toSplitRows(parsed.lines) : []), [split, parsed])

  if (!request) return <div className="git-empty git-empty--fill">Select a file to see what changed in it.</div>

  const toggle = (on: boolean) => {
    setSplit(on)
    try { localStorage.setItem('lucode-git-diff-split', on ? '1' : '0') } catch { /* only a preference */ }
  }
  const added = parsed?.lines.filter(l => l.kind === 'add').length ?? 0
  const removed = parsed?.lines.filter(l => l.kind === 'del').length ?? 0
  const slash = request.path.lastIndexOf('/')

  return (
    <div className="gd">
      <div className="gd__head">
        <span className="gd__path" title={request.path}>
          {slash >= 0 && <span className="gd__dir">{request.path.slice(0, slash + 1)}</span>}
          {request.path.slice(slash + 1)}
        </span>
        {parsed && <Stat added={added} removed={removed} />}
        <div className="git-seg" role="group" aria-label="Diff layout">
          <button className={`git-seg__btn${!split ? ' git-seg__btn--on' : ''}`} onClick={() => toggle(false)} title="Unified" aria-pressed={!split}><IconColumns1 size={14} /></button>
          <button className={`git-seg__btn${split ? ' git-seg__btn--on' : ''}`} onClick={() => toggle(true)} title="Side by side" aria-pressed={split}><IconColumns2 size={14} /></button>
        </div>
      </div>
      {parsed && parsed.notes.length > 0 && <div className="gd__notes">{parsed.notes.join(' · ')}</div>}
      <div className="gd__body">
        {error && <div className="git-msg git-msg--bad">{error}</div>}
        {!answer && !error && (
          <div className="git-skel" aria-label="Loading the diff">
            {[70, 45, 88, 60, 32, 76].map((w, i) => <span key={i} style={{ width: `${w}%` }} />)}
          </div>
        )}
        {parsed?.binary && <div className="git-empty">Binary file: there are no lines to compare.</div>}
        {parsed && !parsed.binary && parsed.lines.length === 0 && !error && (
          <div className="git-empty">{parsed.notes.length ? 'The contents did not change.' : 'No differences.'}</div>
        )}
        {parsed && !split && parsed.lines.length > 0 && (
          <table className="gd-table">
            <tbody>
              {parsed.lines.map((l, i) => (
                l.kind === 'hunk' || l.kind === 'note'
                  ? <tr key={i} className={`gd-row gd-row--${l.kind}`}><td colSpan={3}>{l.kind === 'hunk' ? `@@ ${l.text}` : l.text}</td></tr>
                  : (
                    <tr key={i} className={`gd-row gd-row--${l.kind}`}>
                      <td className="gd-no">{l.oldNo}</td>
                      <td className="gd-no">{l.newNo}</td>
                      <td className="gd-text"><span className="gd-sign" aria-hidden>{SIGN[l.kind]}</span><Code line={l} lang={lang} plain={plain} /></td>
                    </tr>
                  )
              ))}
            </tbody>
          </table>
        )}
        {parsed && split && rows.length > 0 && (
          <table className="gd-table gd-table--split">
            <tbody>
              {rows.map((r, i) => r.full
                ? <tr key={i} className={`gd-row gd-row--${r.full.kind}`}><td colSpan={4}>{r.full.kind === 'hunk' ? `@@ ${r.full.text}` : r.full.text}</td></tr>
                : (
                  <tr key={i} className="gd-row">
                    <td className={`gd-no gd-side--${r.left?.kind ?? 'none'}`}>{r.left?.oldNo}</td>
                    <td className={`gd-text gd-side--${r.left?.kind ?? 'none'}`}>{r.left && <Code line={r.left} lang={lang} plain={plain} />}</td>
                    <td className={`gd-no gd-side--${r.right?.kind ?? 'none'}`}>{r.right?.newNo}</td>
                    <td className={`gd-text gd-side--${r.right?.kind ?? 'none'}`}>{r.right && <Code line={r.right} lang={lang} plain={plain} />}</td>
                  </tr>
                ))}
            </tbody>
          </table>
        )}
        {answer?.truncated && <div className="git-msg">This diff is too long to show whole: it stops here.</div>}
      </div>
    </div>
  )
}
