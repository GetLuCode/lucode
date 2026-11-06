import { memo, useEffect, useMemo, useRef, useState } from 'react'
import type { CSSProperties, KeyboardEvent as ReactKeyboardEvent, PointerEvent as ReactPointerEvent, ReactNode } from 'react'
import { IconArrowBackUp, IconDots, IconGitBranch, IconGitCherryPick, IconLayoutSidebarLeftCollapse, IconLayoutSidebarLeftExpand, IconListDetails, IconListTree, IconLoader2, IconSearch, IconTag, IconTerminal2, IconX } from '@tabler/icons-react'
import { errorText, ago, fullDate, gitCall, shortSha } from './gitApi'
import { layoutGraph } from './gitGraph'
import type { GraphRow, GraphSegment } from './gitGraph'
import { Avatar, FileRow, GitMenu, RefChip, Sha, Stat } from './gitParts'
import type { MenuItem } from './gitParts'
import { DiffView } from './DiffView'
import type { DiffRequest, GitCommit, GitFileStat, GitLog, GitTaskLink } from './gitTypes'
import type { GitUI } from './GitView'

const LANE = 14
const ROW = 46
const NODE_Y = 15
const PAGE = 150
const INLINE_FILES = 5
const MAX_LANES = 18
const MIN_LIST = 180   // the graph and the start of the subject still fit
const MIN_DETAIL = 380 // the same floor Git.css gives the commit details
const LIST_WIDTH_KEY = 'lucode-git-list-width'

function loadListWidth(): number | null {
  try {
    const n = Number(localStorage.getItem(LIST_WIDTH_KEY))
    return n >= MIN_LIST ? n : null
  } catch { return null }
}

function saveListWidth(w: number | null) {
  try {
    if (w === null) localStorage.removeItem(LIST_WIDTH_KEY)
    else localStorage.setItem(LIST_WIDTH_KEY, String(Math.round(w)))
  } catch { /* only a preference */ }
}

const laneX = (lane: number) => LANE / 2 + 3 + lane * LANE
const laneColor = (color: number) => `var(--lane-${color % 8})`

function segmentPath(s: GraphSegment, y0: number, y1: number) {
  const x0 = laneX(s.from)
  const x1 = laneX(s.to)
  if (x0 === x1) return `M${x0} ${y0}L${x1} ${y1}`
  const k = (y1 - y0) * 0.62
  return `M${x0} ${y0}C${x0} ${y0 + k},${x1} ${y1 - k},${x1} ${y1}`
}

// The two pieces of graph of one row: the commit's own height, and straight lines for whatever is
// listed under the commit.
function GraphCell({ row, width, isHead }: { row: GraphRow; width: number; isHead: boolean }) {
  const cx = laneX(row.lane)
  const stroke = laneColor(row.color)
  return (
    <div className="gl-gutter" style={{ width }} aria-hidden>
      <svg width={width} height={ROW} className="gl-svg">
        {row.top.map((s, i) => <path key={`t${i}`} d={segmentPath(s, 0, NODE_Y)} stroke={laneColor(s.color)} />)}
        {row.bottom.map((s, i) => <path key={`b${i}`} d={segmentPath(s, NODE_Y, ROW)} stroke={laneColor(s.color)} />)}
        {isHead && <circle cx={cx} cy={NODE_Y} r={8} className="gl-node-halo" stroke={stroke} />}
        {row.merge
          ? <><circle cx={cx} cy={NODE_Y} r={5} className="gl-node-merge" stroke={stroke} /><circle cx={cx} cy={NODE_Y} r={1.8} fill={stroke} /></>
          : <circle cx={cx} cy={NODE_Y} r={4} className="gl-node" fill={stroke} />}
      </svg>
      <svg width={width} height="100%" className="gl-svg gl-svg--rest" viewBox={`0 0 ${width} 10`} preserveAspectRatio="none">
        {row.through.map(t => (
          <line key={t.lane} x1={laneX(t.lane)} x2={laneX(t.lane)} y1={0} y2={10} stroke={laneColor(t.color)} vectorEffect="non-scaling-stroke" />
        ))}
      </svg>
    </div>
  )
}

const CommitRow = memo(function CommitRow({ c, row, width, selected, showFiles, activeFile, task, onSelect, onFile }: {
  c: GitCommit
  row: GraphRow
  width: number
  selected: boolean
  showFiles: boolean
  activeFile: string
  task?: GitTaskLink
  onSelect: (sha: string) => void
  onFile: (sha: string, f: GitFileStat) => void
}) {
  const isHead = c.refs.some(r => r.head)
  const hidden = c.files_total - Math.min(c.files.length, INLINE_FILES)
  return (
    <div className={`gl-row${selected ? ' gl-row--selected' : ''}${row.merge ? ' gl-row--merge' : ''}`} data-sha={c.sha}>
      <GraphCell row={row} width={width} isHead={isHead} />
      <div className="gl-main">
        <button className="gl-head" onClick={() => onSelect(c.sha)}>
          <span className="gl-title">
            {c.refs.map(r => <RefChip key={`${r.kind}:${r.name}`} r={r} />)}
            {task && <span className="git-task-chip" title={`Task: ${task.title} (${task.list})`}><IconListDetails size={11} />{task.title}</span>}
            <span className="gl-subject">{c.subject}</span>
          </span>
          <span className="gl-meta">
            <Avatar name={c.author} email={c.email} size={15} />
            <span className="gl-author">{c.author}</span>
            <span className="gl-time" title={fullDate(c.at)}>{ago(c.at)}</span>
            <span className="gl-sha">{shortSha(c.sha)}</span>
            {row.merge && <span className="gl-kind">merge</span>}
            {c.files_total > 0 && <span>{c.files_total} {c.files_total === 1 ? 'file' : 'files'}</span>}
            <Stat added={c.added} removed={c.removed} />
          </span>
        </button>
        {showFiles && c.files.length > 0 && (
          <div className="gl-files">
            {c.files.slice(0, INLINE_FILES).map(f => (
              <FileRow key={f.path} file={f} active={selected && activeFile === f.path} onClick={() => onFile(c.sha, f)} />
            ))}
            {hidden > 0 && <button className="gl-more-files" onClick={() => onSelect(c.sha)}>{hidden} more {hidden === 1 ? 'file' : 'files'}</button>}
          </div>
        )}
      </div>
    </div>
  )
})

function CommitDetail({ ui, sha, file, onFile, onSelect, onClose, resizer, narrow, onToggleNarrow }: {
  ui: GitUI
  sha: string
  file: GitFileStat | null
  onFile: (f: GitFileStat) => void
  onSelect: (sha: string) => void
  onClose: () => void
  resizer: ReactNode
  narrow: boolean
  onToggleNarrow: () => void
}) {
  const [commit, setCommit] = useState<GitCommit | null>(null)
  const [error, setError] = useState('')
  const [menu, setMenu] = useState<HTMLElement | null>(null)

  useEffect(() => {
    let cancelled = false
    setCommit(null)
    setError('')
    gitCall<GitCommit>(ui.ctx, 'commit', { sha })
      .then(c => {
        if (cancelled) return
        setCommit(c)
        if (!file && c.files.length > 0) onFile(c.files[0])
      })
      .catch(e => { if (!cancelled) setError(errorText(e)) })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sha, ui.ctx.target?.repo, ui.ctx.worktree])

  const request: DiffRequest | null = file ? { mode: 'commit', sha, path: file.path, orig_path: file.orig_path } : null
  const short = shortSha(sha)
  const items: MenuItem[] = [
    { label: 'Create branch here…', icon: <IconGitBranch size={14} />, onSelect: async () => {
      const name = await ui.prompt({ title: `New branch at ${short}`, label: 'Branch name', action: 'Create branch' })
      if (name) await ui.act('branch_create', { name, start: sha, switch: true })
    } },
    { label: 'Create tag here…', icon: <IconTag size={14} />, onSelect: async () => {
      const name = await ui.prompt({ title: `New tag at ${short}`, label: 'Tag name', action: 'Create tag' })
      if (name) await ui.act('tag', { name, ref: sha })
    } },
    { label: 'Check out this commit', icon: <IconTerminal2 size={14} />, onSelect: () => {
      if (confirm(`Check out ${short}?\n\nHEAD will be detached: new commits belong to no branch until you create one.`)) void ui.act('checkout', { ref: sha, detach: true })
    } },
    { label: `Cherry-pick onto ${ui.overview.branch || 'HEAD'}`, icon: <IconGitCherryPick size={14} />, onSelect: () => {
      if (confirm(`Apply ${short} on top of ${ui.overview.branch || 'HEAD'}?`)) void ui.act('cherry_pick', { ref: sha })
    } },
    { label: 'Revert this commit', icon: <IconArrowBackUp size={14} />, disabled: (commit?.parents.length ?? 0) > 1, onSelect: () => {
      if (confirm(`Create a new commit that undoes ${short}?`)) void ui.act('revert', { ref: sha })
    } },
  ]

  return (
    <aside className="git-detail" aria-label="Commit details">
      {resizer}
      <div className="git-detail__top">
        {error && <div className="git-msg git-msg--bad">{error}</div>}
        {!commit && !error && <div className="git-skel"><span style={{ width: '70%' }} /><span style={{ width: '40%' }} /></div>}
        {commit && (
          <>
            <div className="git-detail__titlebar">
              <button
                className="git-icon-btn git-detail__narrow"
                onClick={onToggleNarrow}
                title={narrow ? 'Widen the branch graph' : 'Collapse the branch graph'}
                aria-label={narrow ? 'Widen the branch graph' : 'Collapse the branch graph'}
              >
                {narrow ? <IconLayoutSidebarLeftExpand size={15} /> : <IconLayoutSidebarLeftCollapse size={15} />}
              </button>
              <h3 className="git-detail__subject">{commit.subject}</h3>
              <button className="git-icon-btn" onClick={e => setMenu(e.currentTarget)} title="Commit actions" aria-label="Commit actions"><IconDots size={15} /></button>
              <button className="git-icon-btn" onClick={onClose} title="Close" aria-label="Close details"><IconX size={15} /></button>
            </div>
            {commit.refs.length > 0 && <div className="git-detail__refs">{commit.refs.map(r => <RefChip key={`${r.kind}:${r.name}`} r={r} />)}</div>}
            {commit.body && <pre className="git-detail__body">{commit.body}</pre>}
            <dl className="git-facts">
              <dt>Author</dt>
              <dd><Avatar name={commit.author} email={commit.email} size={16} /> {commit.author} <span className="git-dim">{commit.email}</span></dd>
              <dt>Date</dt>
              <dd>{fullDate(commit.at)} <span className="git-dim">{ago(commit.at)} ago</span></dd>
              {commit.committer !== commit.author && <><dt>Committer</dt><dd><Avatar name={commit.committer} email={commit.committer_email} size={16} /> {commit.committer}</dd></>}
              <dt>Commit</dt>
              <dd><Sha sha={commit.sha} full /></dd>
              {commit.parents.length > 0 && <>
                <dt>{commit.parents.length > 1 ? 'Parents' : 'Parent'}</dt>
                <dd className="git-facts__parents">{commit.parents.map(p => <button key={p} className="git-link git-mono" onClick={() => onSelect(p)}>{shortSha(p)}</button>)}</dd>
              </>}
            </dl>
            <div className="git-section-head">
              <span>{commit.files_total} {commit.files_total === 1 ? 'file' : 'files'} changed{commit.parents.length > 1 ? ` against ${shortSha(commit.parents[0])}` : ''}</span>
              <Stat added={commit.added} removed={commit.removed} />
            </div>
            <div className="git-detail__files">
              {commit.files.map(f => <FileRow key={f.path} file={f} active={file?.path === f.path} onClick={() => onFile(f)} />)}
            </div>
          </>
        )}
      </div>
      <div className="git-detail__diff"><DiffView ctx={ui.ctx} request={request} /></div>
      {menu && <GitMenu anchor={menu} items={items} onClose={() => setMenu(null)} />}
    </aside>
  )
}

function loadShowFiles() {
  try { return localStorage.getItem('lucode-git-log-files') !== '0' } catch { return true }
}

// The branch graph: every commit on its lane, with what it touched and who did it.
export function HistoryPane({ ui, refName, sha, onRef, onSha, taskByBranch, taskBySha }: {
  ui: GitUI
  refName: string
  sha: string
  onRef: (ref: string) => void
  onSha: (sha: string) => void
  taskByBranch: Map<string, GitTaskLink>
  taskBySha: Map<string, GitTaskLink>
}) {
  const [commits, setCommits] = useState<GitCommit[]>([])
  const [more, setMore] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [grep, setGrep] = useState('')
  const [appliedGrep, setAppliedGrep] = useState('')
  const [showFiles, setShowFiles] = useState(loadShowFiles)
  const [file, setFile] = useState<GitFileStat | null>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const rootRef = useRef<HTMLDivElement>(null)
  const seq = useRef(0)
  const [listWidth, setListWidth] = useState(loadListWidth)

  const clampWidth = (w: number) => Math.max(MIN_LIST, Math.min(w, (rootRef.current?.clientWidth ?? w + MIN_DETAIL) - MIN_DETAIL))
  const applyWidth = (w: number | null) => {
    setListWidth(w)
    saveListWidth(w)
  }
  // Dragging only moves a CSS variable: nothing re-renders the commit list until it is dropped.
  const startResize = (e: ReactPointerEvent<HTMLDivElement>) => {
    if (e.button !== 0) return
    e.preventDefault()
    const handle = e.currentTarget
    const root = rootRef.current
    const startX = e.clientX
    const startW = root?.querySelector('.git-history__list')?.getBoundingClientRect().width ?? MIN_LIST
    let w = startW
    handle.setPointerCapture(e.pointerId)
    root?.classList.add('git-history--resizing')
    const onMove = (ev: PointerEvent) => {
      w = clampWidth(startW + ev.clientX - startX)
      root?.style.setProperty('--git-list-w', `${w}px`)
    }
    const onUp = () => {
      handle.removeEventListener('pointermove', onMove)
      handle.removeEventListener('pointerup', onUp)
      handle.removeEventListener('pointercancel', onUp)
      root?.classList.remove('git-history--resizing')
      if (w !== startW) applyWidth(w)
    }
    handle.addEventListener('pointermove', onMove)
    handle.addEventListener('pointerup', onUp)
    handle.addEventListener('pointercancel', onUp)
  }
  const resizeByKey = (e: ReactKeyboardEvent) => {
    const current = rootRef.current?.querySelector('.git-history__list')?.getBoundingClientRect().width ?? MIN_LIST
    const next = e.key === 'ArrowLeft' ? current - 32 : e.key === 'ArrowRight' ? current + 32 : e.key === 'Home' ? MIN_LIST : null
    if (next === null) return
    e.preventDefault()
    applyWidth(clampWidth(next))
  }
  const narrow = listWidth !== null && listWidth <= MIN_LIST + 24

  const load = async (skip: number) => {
    const my = ++seq.current
    setLoading(true)
    try {
      const res = await gitCall<GitLog>(ui.ctx, 'log', { ref: refName || undefined, grep: appliedGrep || undefined, limit: PAGE, skip })
      if (my !== seq.current) return
      setCommits(prev => (skip ? [...prev, ...res.commits] : res.commits))
      setMore(res.more)
      setError('')
    } catch (e) {
      if (my === seq.current) setError(errorText(e))
    } finally {
      if (my === seq.current) setLoading(false)
    }
  }

  // refsKey changes when any branch moves, also when an agent commits while this is open
  useEffect(() => { void load(0) /* eslint-disable-next-line react-hooks/exhaustive-deps */ }, [ui.refsKey, refName, appliedGrep, ui.ctx.target?.repo, ui.ctx.worktree])

  const trunkTip = useMemo(() => ui.overview.branches.find(b => b.name === ui.overview.main && !b.remote)?.sha, [ui.overview.branches, ui.overview.main])
  const layout = useMemo(() => layoutGraph(commits, appliedGrep ? undefined : trunkTip), [commits, trunkTip, appliedGrep])
  const width = Math.min(layout.lanes, MAX_LANES) * LANE + 8

  // a commit opened from elsewhere (a merge of a worktree) scrolls into view
  useEffect(() => {
    if (!sha) return
    const el = listRef.current?.querySelector(`[data-sha="${sha}"]`)
    el?.scrollIntoView({ block: 'nearest' })
  }, [sha, commits.length])

  const select = (next: string) => {
    if (next !== sha) setFile(null)
    onSha(next === sha ? '' : next)
  }
  const openFile = (next: string, f: GitFileStat) => {
    setFile(f)
    onSha(next)
  }
  const toggleFiles = (on: boolean) => {
    setShowFiles(on)
    try { localStorage.setItem('lucode-git-log-files', on ? '1' : '0') } catch { /* only a preference */ }
  }
  const taskOf = (c: GitCommit) => {
    const direct = taskBySha.get(c.sha)
    if (direct) return direct
    for (const r of c.refs) {
      if (r.kind === 'branch' && taskByBranch.has(r.name)) return taskByBranch.get(r.name)
    }
    return undefined
  }

  return (
    <div
      ref={rootRef}
      className={`git-history${sha ? ' git-history--detail' : ''}`}
      style={listWidth !== null ? { '--git-list-w': `${listWidth}px` } as CSSProperties : undefined}
    >
      <section className="git-history__list" aria-label="Commit history">
        <div className="git-toolbar">
          <div className="git-search">
            <IconSearch size={13} />
            <input
              value={grep}
              onChange={e => setGrep(e.target.value)}
              onKeyDown={e => { e.stopPropagation(); if (e.key === 'Enter') setAppliedGrep(grep.trim()) }}
              placeholder="Search commit messages, Enter"
              aria-label="Search commit messages"
            />
            {appliedGrep && <button className="git-icon-btn git-icon-btn--small" onClick={() => { setGrep(''); setAppliedGrep('') }} aria-label="Clear search"><IconX size={12} /></button>}
          </div>
          {refName
            ? <button className="git-filter" onClick={() => onRef('')} title="Show every branch"><IconGitBranch size={12} />{refName}<IconX size={12} /></button>
            : <span className="git-toolbar__hint">All branches</span>}
          <div className="git-seg" role="group" aria-label="Commit list density">
            <button className={`git-seg__btn${showFiles ? ' git-seg__btn--on' : ''}`} onClick={() => toggleFiles(true)} title="List the files of each commit" aria-pressed={showFiles}><IconListTree size={14} /></button>
            <button className={`git-seg__btn${!showFiles ? ' git-seg__btn--on' : ''}`} onClick={() => toggleFiles(false)} title="Commits only" aria-pressed={!showFiles}><IconListDetails size={14} /></button>
          </div>
        </div>
        <div className="gl" ref={listRef}>
          {error && <div className="git-msg git-msg--bad">{error}</div>}
          {!error && commits.length === 0 && loading && (
            <div className="git-skel git-skel--rows">{Array.from({ length: 9 }, (_, i) => <span key={i} style={{ width: `${40 + ((i * 37) % 50)}%` }} />)}</div>
          )}
          {!error && commits.length === 0 && !loading && (
            <div className="git-empty git-empty--fill">
              {appliedGrep ? `No commit message contains "${appliedGrep}".` : ui.overview.unborn ? 'This repository has no commits yet. Make the first one from Changes.' : 'No commits to show.'}
            </div>
          )}
          {commits.map((c, i) => (
            <CommitRow
              key={c.sha}
              c={c}
              row={layout.rows[i]}
              width={width}
              selected={c.sha === sha}
              showFiles={showFiles}
              activeFile={c.sha === sha ? file?.path ?? '' : ''}
              task={taskOf(c)}
              onSelect={select}
              onFile={openFile}
            />
          ))}
          {more && (
            <button className="git-btn gl-load-more" disabled={loading} onClick={() => void load(commits.length)}>
              {loading && <IconLoader2 size={13} className="git-spin" />}Load {PAGE} more
            </button>
          )}
        </div>
      </section>
      {sha && (
        <CommitDetail
          ui={ui}
          sha={sha}
          file={file}
          onFile={setFile}
          onSelect={next => { setFile(null); onSha(next) }}
          onClose={() => onSha('')}
          narrow={narrow}
          onToggleNarrow={() => applyWidth(narrow ? null : MIN_LIST)}
          resizer={(
            <div
              className="git-resizer"
              role="separator"
              aria-orientation="vertical"
              aria-label="Resize the branch graph"
              title="Drag to resize. Double-click to reset."
              tabIndex={0}
              onPointerDown={startResize}
              onDoubleClick={() => applyWidth(null)}
              onKeyDown={resizeByKey}
            />
          )}
        />
      )}
    </div>
  )
}
