import { useEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties, MouseEvent as ReactMouseEvent, PointerEvent as ReactPointerEvent } from 'react'
import { IconArrowAutofitHeight, IconCode, IconColumns1, IconColumns2, IconCopy, IconSend, IconX } from '@tabler/icons-react'
import { copyText } from '../../lib/copyText'
import { highlightCode } from '../../lib/highlight'
import { useStore } from '../../store'
import { gitCall, errorText } from './gitApi'
import type { GitCallContext } from './gitApi'
import type { DiffRequest, GitDiffAnswer } from './gitTypes'
import { languageOf, parseDiff, toSplitRows } from './gitDiff'
import { buildReference, codeOf, rangeOf, referenceLabel } from './gitRef'
import { CopyButton, GitMenu, Stat } from './gitParts'
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
  // Full height: Git.css hides what sits above the diff in whichever pane holds it.
  const [fullHeight, setFullHeight] = useState(false)
  // Lines picked by their numbers, as positions in the parsed diff: a drag or Shift+click moves `to`.
  const [picked, setPicked] = useState<{ from: number; to: number } | null>(null)
  const dragging = useRef(false)
  // The context menu of the picked lines.
  const [menu, setMenu] = useState<{ x: number; y: number; lines: DiffLine[] } | null>(null)
  const addRef = useStore(s => s.addOrchRef)
  // Esc undoes the nearest thing first: the menu (it closes itself), the selection, the full height.
  useEffect(() => {
    if ((!fullHeight && !picked) || menu) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      if (picked) setPicked(null)
      else setFullHeight(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [fullHeight, picked, menu])
  useEffect(() => {
    const stop = () => { dragging.current = false }
    window.addEventListener('pointerup', stop)
    window.addEventListener('pointercancel', stop)
    return () => { window.removeEventListener('pointerup', stop); window.removeEventListener('pointercancel', stop) }
  }, [])
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
  // The number columns take what the numbers need, and the code keeps the rest.
  const tableStyle = useMemo(() => {
    const highest = (parsed?.lines ?? []).reduce((m, l) => Math.max(m, l.oldNo ?? 0, l.newNo ?? 0), 0)
    return { '--gd-no': `calc(${Math.max(2, String(highest).length)}ch + 14px)` } as CSSProperties
  }, [parsed])
  // A selection means nothing in other text: a refresh that changes the diff, or another file, drops it.
  useEffect(() => { setPicked(null); setMenu(null) }, [answer?.text, request?.path])
  const positions = useMemo(() => new Map((parsed?.lines ?? []).map((l, i) => [l, i])), [parsed])
  const low = picked ? Math.min(picked.from, picked.to) : -1
  const high = picked ? Math.max(picked.from, picked.to) : -1
  const pickedLines = useMemo(() => (parsed && picked ? parsed.lines.slice(low, high + 1) : []), [parsed, picked, low, high])
  const isPicked = (l?: DiffLine) => {
    const i = l ? positions.get(l) : undefined
    return i !== undefined && i >= low && i <= high && picked !== null
  }
  // Right-clicking a line outside the selection picks that line, as file managers do.
  const openMenu = (e: ReactMouseEvent, at: number) => {
    e.preventDefault()
    const inside = picked !== null && at >= low && at <= high
    if (!inside) setPicked({ from: at, to: at })
    setMenu({ x: e.clientX, y: e.clientY, lines: inside ? pickedLines : parsed ? [parsed.lines[at]] : [] })
  }
  // On the code, a picked line opens the menu too, unless there is text selected to copy by hand.
  const codeProps = (l?: DiffLine) => {
    const at = l ? positions.get(l) : undefined
    if (at === undefined || !isPicked(l)) return {}
    return {
      onContextMenu: (e: ReactMouseEvent) => {
        if (!window.getSelection()?.isCollapsed) return
        openMenu(e, at)
      },
    }
  }
  const numberProps = (l?: DiffLine) => {
    const at = l ? positions.get(l) : undefined
    if (at === undefined) return {}
    return {
      title: 'Click to pick this line, Shift+click or drag for several. Right-click for actions.',
      onContextMenu: (e: ReactMouseEvent) => openMenu(e, at),
      onPointerDown: (e: ReactPointerEvent) => {
        if (e.button !== 0) return
        e.preventDefault()
        dragging.current = true
        setPicked(prev => (e.shiftKey && prev ? { from: prev.from, to: at } : prev && prev.from === at && prev.to === at ? null : { from: at, to: at }))
      },
      onPointerEnter: () => {
        if (dragging.current) setPicked(prev => (prev ? { from: prev.from, to: at } : { from: at, to: at }))
      },
    }
  }

  if (!request) return <div className="git-empty git-empty--fill">Select a file to see what changed in it.</div>

  const toggle = (on: boolean) => {
    setSplit(on)
    try { localStorage.setItem('lucode-git-diff-split', on ? '1' : '0') } catch { /* only a preference */ }
  }
  const added = parsed?.lines.filter(l => l.kind === 'add').length ?? 0
  const removed = parsed?.lines.filter(l => l.kind === 'del').length ?? 0
  const slash = request.path.lastIndexOf('/')
  const send = (lines: DiffLine[]) => addRef({ projectId: ctx.projectId, ...buildReference(request, lines, ctx.target, ctx.worktree) })
  const menuLabel = menu ? referenceLabel(request, rangeOf(menu.lines)) : ''

  return (
    <div className={`gd${fullHeight ? ' gd--full' : ''}`}>
      <div className="gd__head">
        <span className="gd__path" title={request.path}>
          {slash >= 0 && <span className="gd__dir">{request.path.slice(0, slash + 1)}</span>}
          {request.path.slice(slash + 1)}
        </span>
        <CopyButton text={request.path} label="Copy path" className="git-icon-btn gd__ref-btn" />
        <button
          className="git-icon-btn gd__ref-btn gd__send-file"
          onClick={() => send([])}
          title="Send this file to the Supervisor"
          aria-label="Send this file to the Supervisor"
        ><IconSend size={13} /></button>
        {parsed && <Stat added={added} removed={removed} />}
        <div className="git-seg" role="group" aria-label="Diff layout">
          <button className={`git-seg__btn${!split ? ' git-seg__btn--on' : ''}`} onClick={() => toggle(false)} title="Unified" aria-pressed={!split}><IconColumns1 size={14} /></button>
          <button className={`git-seg__btn${split ? ' git-seg__btn--on' : ''}`} onClick={() => toggle(true)} title="Side by side" aria-pressed={split}><IconColumns2 size={14} /></button>
        </div>
        <button
          className={`git-icon-btn gd__full${fullHeight ? ' gd__full--on' : ''}`}
          onClick={() => setFullHeight(v => !v)}
          aria-pressed={fullHeight}
          aria-label="Full height"
          title={fullHeight ? 'Back to normal height (Esc)' : 'Full height'}
        ><IconArrowAutofitHeight size={15} /></button>
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
          <table className="gd-table" style={tableStyle}>
            <tbody>
              {parsed.lines.map((l, i) => (
                l.kind === 'hunk' || l.kind === 'note'
                  ? <tr key={i} className={`gd-row gd-row--${l.kind}`}><td colSpan={3}>{l.kind === 'hunk' ? `@@ ${l.text}` : l.text}</td></tr>
                  : (
                    <tr key={i} className={`gd-row gd-row--${l.kind}${isPicked(l) ? ' gd-row--sel' : ''}`}>
                      <td className="gd-no gd-no--pick" {...numberProps(l)}>{l.oldNo}</td>
                      <td className="gd-no gd-no--pick" {...numberProps(l)}>{l.newNo}</td>
                      <td className="gd-text" {...codeProps(l)}><span className="gd-sign" aria-hidden>{SIGN[l.kind]}</span><Code line={l} lang={lang} plain={plain} /></td>
                    </tr>
                  )
              ))}
            </tbody>
          </table>
        )}
        {parsed && split && rows.length > 0 && (
          <table className="gd-table gd-table--split" style={tableStyle}>
            {/* With a fixed layout the first row sets the widths, and that row is a hunk header
                spanning all four columns: without these, each column gets a quarter. */}
            <colgroup>
              <col className="gd-col-no" />
              <col />
              <col className="gd-col-no" />
              <col />
            </colgroup>
            <tbody>
              {rows.map((r, i) => r.full
                ? <tr key={i} className={`gd-row gd-row--${r.full.kind}`}><td colSpan={4}>{r.full.kind === 'hunk' ? `@@ ${r.full.text}` : r.full.text}</td></tr>
                : (
                  <tr key={i} className="gd-row">
                    <td className={`gd-no gd-side--${r.left?.kind ?? 'none'}${r.left ? ' gd-no--pick' : ''}${isPicked(r.left) ? ' gd-cell--sel' : ''}`} {...numberProps(r.left)}>{r.left?.oldNo}</td>
                    <td className={`gd-text gd-side--${r.left?.kind ?? 'none'}${isPicked(r.left) ? ' gd-cell--sel' : ''}`} {...codeProps(r.left)}>{r.left && <Code line={r.left} lang={lang} plain={plain} />}</td>
                    <td className={`gd-no gd-side--${r.right?.kind ?? 'none'}${r.right ? ' gd-no--pick' : ''}${isPicked(r.right) ? ' gd-cell--sel' : ''}`} {...numberProps(r.right)}>{r.right?.newNo}</td>
                    <td className={`gd-text gd-side--${r.right?.kind ?? 'none'}${isPicked(r.right) ? ' gd-cell--sel' : ''}`} {...codeProps(r.right)}>{r.right && <Code line={r.right} lang={lang} plain={plain} />}</td>
                  </tr>
                ))}
            </tbody>
          </table>
        )}
        {answer?.truncated && <div className="git-msg">This diff is too long to show whole: it stops here.</div>}
      </div>
      {menu && (
        <GitMenu
          point={menu}
          header={menuLabel}
          onClose={() => setMenu(null)}
          items={[
            { label: 'Copy reference', icon: <IconCopy size={14} />, onSelect: () => void copyText(menuLabel) },
            { label: 'Copy code', icon: <IconCode size={14} />, onSelect: () => void copyText(codeOf(menu.lines)) },
            { label: 'Send to Supervisor', icon: <IconSend size={14} />, onSelect: () => { send(menu.lines); setPicked(null) } },
            { label: 'Clear selection', icon: <IconX size={14} />, onSelect: () => setPicked(null) },
          ]}
        />
      )}
    </div>
  )
}
