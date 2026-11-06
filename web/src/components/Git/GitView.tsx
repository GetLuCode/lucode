import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import {
  IconAlertTriangle, IconArchive, IconArrowDown, IconArrowUp, IconCheck, IconChevronDown, IconChevronRight, IconCloudDownload,
  IconDots, IconEdit, IconFolders, IconGitBranch, IconGitCompare, IconGitMerge, IconHistory, IconLoader2, IconPencilPlus, IconPlus,
  IconRefresh, IconServer, IconTag, IconTrash, IconWorld, IconX,
} from '@tabler/icons-react'
import { useStore } from '../../store'
import * as api from '../../api'
import { ago, errorText, gitCall, shortSha } from './gitApi'
import type { GitCallContext } from './gitApi'
import { GitMenu, GitPrompt } from './gitParts'
import type { MenuItem, PromptOptions } from './gitParts'
import { HistoryPane } from './HistoryPane'
import { ChangesPane } from './ChangesPane'
import { ComparePane } from './ComparePane'
import { WorktreesPane } from './WorktreesPane'
import { StashPane } from './StashPane'
import type { GitBranch, GitOverview, GitTarget, GitTargets, GitTaskLink, GitWorktrees } from './gitTypes'
import './Git.css'

type View = 'changes' | 'history' | 'compare' | 'worktrees' | 'stash'
const POLL_MS = 8000

// What the panes need from the shell.
export interface GitUI {
  ctx: GitCallContext
  overview: GitOverview
  refsKey: string // changes when any branch, tag or HEAD moves
  busy: string
  act: (action: string, body?: Record<string, unknown>) => Promise<boolean>
  prompt: (o: PromptOptions) => Promise<string | null>
}

const BUSY_LABEL: Record<string, string> = { fetch: 'Fetching', pull: 'Pulling', push: 'Pushing', merge: 'Merging', commit: 'Committing', stage: 'Staging', unstage: 'Unstaging', checkout: 'Checking out' }

function Section({ title, count, defaultOpen, action, children }: { title: string; count: number; defaultOpen: boolean; action?: React.ReactNode; children: React.ReactNode }) {
  const [open, setOpen] = useState(defaultOpen)
  return (
    <section className="gside-section">
      <header className="gside-section__head">
        <button className="gside-section__toggle" onClick={() => setOpen(v => !v)} aria-expanded={open}>
          {open ? <IconChevronDown size={12} /> : <IconChevronRight size={12} />}
          {title}<span className="git-count">{count}</span>
        </button>
        {action}
      </header>
      {open && children}
    </section>
  )
}

function BranchRow({ b, active, onOpen, onMenu, main }: { b: GitBranch; active: boolean; onOpen: () => void; onMenu: (el: HTMLElement) => void; main: string }) {
  const parts = b.name.split('/')
  const leaf = parts.pop()
  const showMain = !b.remote && b.name !== main && (b.main_ahead > 0 || b.main_behind > 0)
  return (
    <div className={`gside-row${active ? ' gside-row--active' : ''}${b.current ? ' gside-row--current' : ''}`}>
      <button className="gside-row__main" onClick={onOpen} title={`${b.name}\n${b.subject}\n${b.author}, ${ago(b.at)} ago`}>
        {b.current ? <IconCheck size={13} className="gside-row__mark" /> : <IconGitBranch size={13} className="gside-row__icon" />}
        <span className="gside-row__name">
          {parts.length > 0 && <span className="git-path__dir">{parts.join('/')}/</span>}
          {leaf}
        </span>
        {b.worktree_path && !b.current && <IconFolders size={12} className="gside-row__icon" aria-label="Checked out in a worktree" />}
        {showMain && (
          <span className="gside-ab" title={`${b.main_ahead} ahead, ${b.main_behind} behind ${main}`}>
            {b.main_ahead > 0 && <span>↑{b.main_ahead}</span>}
            {b.main_behind > 0 && <span>↓{b.main_behind}</span>}
          </span>
        )}
      </button>
      <button className="git-icon-btn git-icon-btn--small gside-row__more" onClick={e => onMenu(e.currentTarget)} aria-label={`Actions for ${b.name}`}><IconDots size={13} /></button>
    </div>
  )
}

export function GitView() {
  const projectId = useStore(s => s.activeProjectId)
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()

  const [targets, setTargets] = useState<GitTargets | null>(null)
  const [targetIdx, setTargetIdx] = useState(0)
  const [worktree, setWorktree] = useState('')
  const [overview, setOverview] = useState<GitOverview | null>(null)
  const [loadError, setLoadError] = useState('')
  const [actionError, setActionError] = useState('')
  const [notice, setNotice] = useState('')
  const [busy, setBusy] = useState('')
  const [refreshing, setRefreshing] = useState(false)
  const [branchFilter, setBranchFilter] = useState('')
  const [refsOpen, setRefsOpen] = useState(false)
  const [menu, setMenu] = useState<{ anchor: HTMLElement; items: MenuItem[]; header?: string } | null>(null)
  const [promptState, setPromptState] = useState<{ o: PromptOptions; resolve: (v: string | null) => void } | null>(null)
  const [worktrees, setWorktrees] = useState<GitWorktrees | null>(null)
  const [worktreesError, setWorktreesError] = useState('')

  const requested = params.get('view') as View
  const view: View = ['changes', 'history', 'compare', 'worktrees', 'stash'].includes(requested) ? requested : 'changes'
  const refName = params.get('ref') ?? ''
  const sha = params.get('sha') ?? ''
  const stashRef = params.get('stash') ?? ''
  const base = params.get('base') ?? ''
  const head = params.get('head') ?? ''

  const go = useCallback((next: Record<string, string>) => {
    setParams(prev => {
      const p = new URLSearchParams(prev)
      for (const [k, v] of Object.entries(next)) {
        if (v) p.set(k, v)
        else p.delete(k)
      }
      return p
    }, { replace: true })
  }, [setParams])

  useEffect(() => {
    if (!projectId) return
    let cancelled = false
    setTargets(null)
    setOverview(null)
    setTargetIdx(0)
    setWorktree('')
    api.getGitTargets<GitTargets>(projectId)
      .then(t => { if (!cancelled) setTargets(t) })
      .catch(e => { if (!cancelled) setTargets({ targets: [], reason: errorText(e) }) })
    return () => { cancelled = true }
  }, [projectId])

  const target: GitTarget | null = targets?.targets[targetIdx] ?? null
  const ctx: GitCallContext = useMemo(() => ({ projectId: projectId ?? '', target, worktree }), [projectId, target, worktree])
  const ctxRef = useRef(ctx)
  ctxRef.current = ctx
  const lastRaw = useRef('')

  const refresh = useCallback(async () => {
    const my = ctxRef.current
    if (!my.target) return
    setRefreshing(true)
    try {
      const ov = await gitCall<GitOverview>(my, 'overview')
      if (ctxRef.current !== my) return // the user moved to another repository meanwhile
      setLoadError('')
      const raw = JSON.stringify(ov)
      if (raw !== lastRaw.current) { // a repeat of the same answer must not re-render the graph
        lastRaw.current = raw
        setOverview(ov)
      }
    } catch (e) {
      if (ctxRef.current === my) setLoadError(errorText(e))
    } finally {
      if (ctxRef.current === my) setRefreshing(false)
    }
  }, [])

  useEffect(() => {
    lastRaw.current = ''
    setOverview(null)
    setWorktrees(null)
    setLoadError('')
    void refresh()
    const tick = () => { if (!document.hidden) void refresh() }
    const id = setInterval(tick, POLL_MS)
    window.addEventListener('focus', tick)
    return () => { clearInterval(id); window.removeEventListener('focus', tick) }
  }, [ctx, refresh])

  const refsKey = useMemo(() => {
    if (!overview) return ''
    return [overview.head, overview.branch, ...overview.branches.map(b => b.name + b.sha), ...overview.tags.map(t => t.name + t.sha)].join('|')
  }, [overview])
  const statusKey = useMemo(() => JSON.stringify(overview?.changes ?? []), [overview])
  const repoWide = view === 'worktrees' ? statusKey : ''

  // Worktrees and their tasks also put task names on the graph, so they load for History as well.
  useEffect(() => {
    if (!overview || overview.not_repo || (view !== 'worktrees' && view !== 'history')) return
    let cancelled = false
    gitCall<GitWorktrees>(ctx, 'worktrees')
      .then(w => { if (!cancelled) { setWorktrees(w); setWorktreesError('') } })
      .catch(e => { if (!cancelled) setWorktreesError(errorText(e)) })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [refsKey, repoWide, view === 'worktrees' || view === 'history', ctx])

  const { taskByBranch, taskBySha } = useMemo(() => {
    const byBranch = new Map<string, GitTaskLink>()
    const bySha = new Map<string, GitTaskLink>()
    for (const w of worktrees?.worktrees ?? []) {
      if (w.task && w.branch) byBranch.set(w.branch, w.task)
      if (w.task && w.merge?.kind !== 'contained' && w.merge?.sha) bySha.set(w.merge.sha, w.task)
    }
    for (const t of worktrees?.merged_tasks ?? []) {
      if (t.merge?.sha) bySha.set(t.merge.sha, t)
    }
    return { taskByBranch: byBranch, taskBySha: bySha }
  }, [worktrees])

  const act = useCallback(async (action: string, body: Record<string, unknown> = {}) => {
    setBusy(BUSY_LABEL[action] ?? 'Working')
    setActionError('')
    setNotice('')
    try {
      const res = await gitCall<{ output: string }>(ctxRef.current, 'action', { action, ...body })
      if (action === 'fetch' || action === 'pull' || action === 'push') setNotice(`${BUSY_LABEL[action].replace('ing', '')} finished.${res.output ? ` ${res.output.split('\n').pop()}` : ''}`)
      return true
    } catch (e) {
      setActionError(errorText(e))
      return false
    } finally {
      setBusy('')
      await refresh()
    }
  }, [refresh])

  const prompt = useCallback((o: PromptOptions) => new Promise<string | null>(resolve => setPromptState({ o, resolve })), [])

  const onBranchMenu = useCallback((b: GitBranch, anchor: HTMLElement) => {
    const ov = overview
    if (!ov) return
    const current = ov.branch
    const items: MenuItem[] = []
    if (!b.current) {
      items.push({
        label: b.remote ? 'Check out as local branch' : 'Check out',
        icon: <IconCheck size={14} />,
        disabled: !!b.worktree_path,
        onSelect: () => void act('checkout', { ref: b.remote ? b.name.slice(b.name.indexOf('/') + 1) : b.name }),
      })
    }
    items.push({ label: `Compare with ${ov.main}`, icon: <IconGitCompare size={14} />, onSelect: () => go({ view: 'compare', base: ov.main, head: b.name }) })
    if (!b.current && current) {
      items.push({
        label: `Merge into ${current}`,
        icon: <IconGitMerge size={14} />,
        onSelect: () => {
          if (confirm(`Merge ${b.name} into ${current}?\n\nIf git finds conflicts the merge stays open here to resolve them, or to abort it.`)) void act('merge', { ref: b.name, no_ff: true })
        },
      })
    }
    items.push({
      label: 'New branch from here…',
      icon: <IconPlus size={14} />,
      onSelect: async () => {
        const name = await prompt({ title: `New branch from ${b.name}`, label: 'Branch name', action: 'Create branch' })
        if (name) await act('branch_create', { name, start: b.name, switch: true })
      },
    })
    if (!b.remote) {
      items.push({
        label: 'Rename…',
        icon: <IconEdit size={14} />,
        onSelect: async () => {
          const name = await prompt({ title: `Rename ${b.name}`, label: 'New name', initial: b.name, action: 'Rename' })
          if (name && name !== b.name) await act('branch_rename', { ref: b.name, name })
        },
      })
      if (!b.current) {
        items.push({
          label: 'Delete',
          icon: <IconTrash size={14} />,
          danger: true,
          disabled: !!b.worktree_path,
          onSelect: () => {
            const unmerged = b.main_ahead > 0
            const text = unmerged
              ? `Delete ${b.name}?\n\nIts ${b.main_ahead} ${b.main_ahead === 1 ? 'commit is' : 'commits are'} not in ${ov.main} and will only be reachable through the reflog.`
              : `Delete ${b.name}?`
            if (confirm(text)) void act('branch_delete', { ref: b.name, force: unmerged })
          },
        })
      }
    }
    setMenu({ anchor, items, header: b.name })
  }, [overview, act, go, prompt])

  const ui: GitUI | null = useMemo(() => (overview ? { ctx, overview, refsKey, busy, act, prompt } : null), [ctx, overview, refsKey, busy, act, prompt])

  const { local, remote } = useMemo(() => {
    const q = branchFilter.trim().toLowerCase()
    const all = (overview?.branches ?? []).filter(b => !q || b.name.toLowerCase().includes(q))
    const rank = (b: GitBranch) => (b.current ? 0 : b.name === overview?.main ? 1 : 2)
    return {
      local: all.filter(b => !b.remote).sort((a, b) => rank(a) - rank(b)),
      remote: all.filter(b => b.remote),
    }
  }, [overview, branchFilter])

  if (!targets) return <div className="git-center"><div className="git-skel git-skel--rows" style={{ width: 320 }}><span style={{ width: '80%' }} /><span style={{ width: '55%' }} /></div></div>
  if (targets.targets.length === 0) {
    return (
      <div className="git-center">
        <div className="git-empty">
          <IconGitBranch size={22} />
          <strong>No repository to show</strong>
          <span>{targets.reason}</span>
        </div>
      </div>
    )
  }

  const changeCount = overview?.changes.length ?? 0
  const selectedStash = overview?.stashes.find(s => s.ref === stashRef) ?? null
  const scopeLabel = worktree ? worktree.split(/[\\/]/).pop() : 'Main checkout'
  const conflicts = overview?.changes.filter(c => c.conflict).length ?? 0

  const NAV: { id: View; label: string; icon: React.ReactNode; count?: number }[] = [
    { id: 'changes', label: 'Changes', icon: <IconPencilPlus size={15} />, count: changeCount },
    { id: 'history', label: 'History', icon: <IconHistory size={15} /> },
    { id: 'compare', label: 'Compare', icon: <IconGitCompare size={15} /> },
    { id: 'worktrees', label: 'Worktrees', icon: <IconFolders size={15} />, count: overview?.worktrees.length },
  ]

  return (
    <div className="git">
      <header className="git-bar">
        <label className="git-bar__repo" title={target ? `${target.hostname || target.runner_id}: ${target.repo}` : ''}>
          <IconServer size={14} className="git-dim" />
          <select
            className="git-select git-select--bare"
            value={targetIdx}
            aria-label="Repository"
            onChange={e => { setTargetIdx(Number(e.target.value)); setWorktree('') }}
          >
            {targets.targets.map((t, i) => (
              <option key={`${t.runner_id}:${t.repo}`} value={i}>{t.repo.split(/[\\/]/).filter(Boolean).pop() || t.repo} · {t.hostname || t.runner_id.slice(0, 8)}{t.online ? '' : ' (offline)'}</option>
            ))}
          </select>
        </label>
        {overview && overview.worktrees.length > 1 && (
          <label className="git-bar__scope">
            <IconFolders size={14} className="git-dim" />
            <select
              className="git-select git-select--bare"
              value={worktree}
              aria-label="Checkout to work in"
              title={scopeLabel}
              onChange={e => setWorktree(e.target.value)}
            >
              {overview.worktrees.map(w => (
                <option key={w.path} value={w.main ? '' : w.path}>{w.main ? 'Main checkout' : (w.branch || w.path.split(/[\\/]/).pop())}</option>
              ))}
            </select>
          </label>
        )}
        {overview && !overview.not_repo && (
          <>
            <button
              className="git-bar__branch"
              title="Switch branch"
              onClick={e => {
                const items: MenuItem[] = (overview.branches.filter(b => !b.remote)).slice(0, 14).map(b => ({
                  label: b.name,
                  icon: b.current ? <IconCheck size={14} /> : <IconGitBranch size={14} />,
                  disabled: b.current || !!b.worktree_path,
                  onSelect: () => void act('checkout', { ref: b.name }),
                }))
                items.push({
                  label: 'New branch…',
                  icon: <IconPlus size={14} />,
                  onSelect: async () => {
                    const name = await prompt({ title: 'New branch', label: `Branch name, from ${overview.branch || shortSha(overview.head)}`, action: 'Create branch' })
                    if (name) await act('branch_create', { name, switch: true })
                  },
                })
                setMenu({ anchor: e.currentTarget, items })
              }}
            >
              <IconGitBranch size={14} />
              <span className="git-bar__branch-name">{overview.branch || `detached ${shortSha(overview.head)}`}</span>
              {overview.head && <span className="git-bar__head">{shortSha(overview.head)}</span>}
              <IconChevronDown size={12} />
            </button>
            {overview.upstream
              ? <span className="git-bar__upstream" title={`Tracking ${overview.upstream}`}><IconWorld size={13} />{overview.upstream}</span>
              : overview.remotes.length > 0 && <span className="git-bar__upstream git-dim">not published</span>}
            <div className="git-bar__end">
              {busy && <span className="git-bar__busy"><IconLoader2 size={13} className="git-spin" />{busy}…</span>}
              <button className="git-btn" disabled={!!busy || overview.remotes.length === 0} onClick={() => void act('fetch')} title="git fetch --all --prune"><IconCloudDownload size={14} />Fetch</button>
              <button className="git-btn" disabled={!!busy || !overview.upstream} onClick={() => void act('pull')} title="git pull --ff-only">
                <IconArrowDown size={14} />Pull{overview.behind > 0 && <span className="git-count git-count--on">{overview.behind}</span>}
              </button>
              <button className="git-btn" disabled={!!busy || overview.remotes.length === 0 || overview.detached} onClick={() => void act('push')} title={overview.upstream ? 'git push' : 'git push --set-upstream'}>
                <IconArrowUp size={14} />{overview.upstream ? 'Push' : 'Publish'}{overview.ahead > 0 && <span className="git-count git-count--on">{overview.ahead}</span>}
              </button>
              <button className="git-icon-btn" onClick={() => void refresh()} title="Refresh" aria-label="Refresh"><IconRefresh size={15} className={refreshing ? 'git-spin' : undefined} /></button>
            </div>
          </>
        )}
      </header>

      {overview?.state && (
        <div className="git-banner git-banner--warn" role="status">
          <IconAlertTriangle size={15} />
          <span>
            <b>{overview.state[0].toUpperCase() + overview.state.slice(1)}</b>
            {conflicts > 0 ? ` with ${conflicts} ${conflicts === 1 ? 'conflict' : 'conflicts'}. Fix them in the files, stage them and commit.` : '. Commit in Changes to finish.'}
          </span>
          {overview.state !== 'bisecting' && (
            <button className="git-btn git-btn--danger" disabled={!!busy} onClick={() => { if (confirm(`Abort: go back to how things were before ${overview.state}?`)) void act('abort') }}>Abort</button>
          )}
        </div>
      )}
      {actionError && (
        <div className="git-banner git-banner--bad" role="alert">
          <IconAlertTriangle size={15} /><pre>{actionError}</pre>
          <button className="git-icon-btn git-icon-btn--small" onClick={() => setActionError('')} aria-label="Dismiss"><IconX size={13} /></button>
        </div>
      )}
      {notice && !actionError && (
        <div className="git-banner" role="status">
          <IconCheck size={15} /><pre>{notice}</pre>
          <button className="git-icon-btn git-icon-btn--small" onClick={() => setNotice('')} aria-label="Dismiss"><IconX size={13} /></button>
        </div>
      )}

      {loadError && !overview && (
        <div className="git-center">
          <div className="git-empty">
            <IconAlertTriangle size={22} />
            <strong>Could not read this repository</strong>
            <span>{loadError}</span>
            <button className="git-btn" onClick={() => void refresh()}><IconRefresh size={14} />Try again</button>
          </div>
        </div>
      )}
      {loadError && overview && <div className="git-banner git-banner--bad" role="alert"><IconAlertTriangle size={15} /><pre>Showing the last known state. {loadError}</pre></div>}
      {!overview && !loadError && <div className="git-center"><div className="git-skel git-skel--rows" style={{ width: 320 }}><span style={{ width: '80%' }} /><span style={{ width: '55%' }} /><span style={{ width: '66%' }} /></div></div>}
      {overview?.not_repo && (
        <div className="git-center">
          <div className="git-empty">
            <IconGitBranch size={22} />
            <strong>This directory is not a git repository</strong>
            <span className="git-mono">{overview.dir}</span>
          </div>
        </div>
      )}

      {overview && !overview.not_repo && ui && (
        <div className="git-body">
          <nav className="gside" aria-label="Repository">
            <div className="gside-nav">
              {NAV.map(n => (
                <button
                  key={n.id}
                  className={`gside-nav__item${view === n.id ? ' gside-nav__item--on' : ''}`}
                  aria-current={view === n.id ? 'page' : undefined}
                  onClick={() => go({ view: n.id, stash: '' })}
                >
                  {n.icon}<span>{n.label}</span>
                  {!!n.count && <span className={`git-count${n.id === 'changes' ? ' git-count--on' : ''}`}>{n.count}</span>}
                </button>
              ))}
              {/* at phone width the lists below would leave no room for the work: they open on demand */}
              <button className={`gside-nav__item gside-nav__refs${refsOpen ? ' gside-nav__item--on' : ''}`} aria-expanded={refsOpen} onClick={() => setRefsOpen(v => !v)}>
                <IconGitBranch size={15} /><span>Branches</span><span className="git-count">{local.length}</span>
              </button>
            </div>
            <div className={`gside-scroll${refsOpen ? ' gside-scroll--open' : ''}`} onClick={e => { if ((e.target as HTMLElement).closest('.gside-row__main')) setRefsOpen(false) }}>
              <div className="gside-filter">
                <input
                  value={branchFilter}
                  onChange={e => setBranchFilter(e.target.value)}
                  onKeyDown={e => e.stopPropagation()}
                  placeholder="Filter branches"
                  aria-label="Filter branches"
                />
              </div>
              <Section title="Branches" count={local.length} defaultOpen>
                {local.map(b => (
                  <BranchRow
                    key={b.name}
                    b={b}
                    main={overview.main}
                    active={view === 'history' && refName === b.name}
                    onOpen={() => go({ view: 'history', ref: b.name, sha: '', stash: '' })}
                    onMenu={el => onBranchMenu(b, el)}
                  />
                ))}
              </Section>
              {remote.length > 0 && (
                <Section title="Remote" count={remote.length} defaultOpen={false}>
                  {remote.map(b => (
                    <BranchRow
                      key={b.name}
                      b={b}
                      main={overview.main}
                      active={view === 'history' && refName === b.name}
                      onOpen={() => go({ view: 'history', ref: b.name, sha: '', stash: '' })}
                      onMenu={el => onBranchMenu(b, el)}
                    />
                  ))}
                </Section>
              )}
              <Section title="Stashes" count={overview.stashes.length} defaultOpen={overview.stashes.length > 0}>
                {overview.stashes.length === 0 && <div className="gside-none">Nothing stashed.</div>}
                {overview.stashes.map(s => (
                  <div key={s.ref} className={`gside-row${view === 'stash' && stashRef === s.ref ? ' gside-row--active' : ''}`}>
                    <button className="gside-row__main" onClick={() => go({ view: 'stash', stash: s.ref })} title={s.message}>
                      <IconArchive size={13} className="gside-row__icon" />
                      <span className="gside-row__name">{s.message.replace(/^(On|WIP on) [^:]+: /, '')}</span>
                      <span className="gside-ab">{ago(s.at)}</span>
                    </button>
                  </div>
                ))}
              </Section>
              {overview.tags.length > 0 && (
                <Section title="Tags" count={overview.tags.length} defaultOpen={false}>
                  {overview.tags.map(t => (
                    <div key={t.name} className="gside-row">
                      <button className="gside-row__main" onClick={() => go({ view: 'history', ref: '', sha: t.sha, stash: '' })} title={t.subject}>
                        <IconTag size={13} className="gside-row__icon" />
                        <span className="gside-row__name">{t.name}</span>
                      </button>
                      <button className="git-icon-btn git-icon-btn--small gside-row__more" aria-label={`Actions for ${t.name}`} onClick={e => setMenu({
                        anchor: e.currentTarget,
                        header: t.name,
                        items: [
                          { label: `Compare with ${overview.main}`, icon: <IconGitCompare size={14} />, onSelect: () => go({ view: 'compare', base: overview.main, head: t.name }) },
                          { label: 'Delete tag', icon: <IconTrash size={14} />, danger: true, onSelect: () => { if (confirm(`Delete the tag ${t.name}?`)) void act('tag_delete', { ref: t.name }) } },
                        ],
                      })}><IconDots size={13} /></button>
                    </div>
                  ))}
                </Section>
              )}
            </div>
          </nav>
          <main className="git-main">
            {view === 'changes' && <ChangesPane ui={ui} statusKey={statusKey} />}
            {view === 'history' && (
              <HistoryPane
                ui={ui}
                refName={refName}
                sha={sha}
                onRef={r => go({ ref: r, sha: '' })}
                onSha={s => go({ sha: s })}
                taskByBranch={taskByBranch}
                taskBySha={taskBySha}
              />
            )}
            {view === 'compare' && (
              <ComparePane ui={ui} base={base} head={head} onChange={(b, h) => go({ base: b, head: h })} onOpenCommit={s => go({ view: 'history', ref: '', sha: s })} />
            )}
            {view === 'worktrees' && (
              <WorktreesPane
                ui={ui}
                data={worktrees}
                error={worktreesError}
                onCompare={b => go({ view: 'compare', base: overview.main, head: b })}
                onOpenCommit={s => go({ view: 'history', ref: '', sha: s })}
                onSession={id => navigate(`/projects/${projectId}/sessions/${id}`)}
                onUse={p => { setWorktree(p); go({ view: 'changes' }) }}
              />
            )}
            {view === 'stash' && (selectedStash
              ? <StashPane ui={ui} stash={selectedStash} />
              : <div className="git-empty git-empty--fill">That stash is gone. Pick another from the list.</div>)}
          </main>
        </div>
      )}

      {menu && <GitMenu anchor={menu.anchor} items={menu.items} header={menu.header} onClose={() => setMenu(null)} />}
      {promptState && <GitPrompt o={promptState.o} onDone={v => { promptState.resolve(v); setPromptState(null) }} />}
    </div>
  )
}
