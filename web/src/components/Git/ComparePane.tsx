import { useEffect, useMemo, useState } from 'react'
import { IconAlertTriangle, IconArrowsExchange, IconCircleCheck, IconGitMerge, IconGitBranch } from '@tabler/icons-react'
import { ago, errorText, fullDate, gitCall, shortSha } from './gitApi'
import { Avatar, FileRow, RefChip, Stat } from './gitParts'
import { DiffView } from './DiffView'
import type { DiffRequest, GitCommit, GitCompare, GitFileStat } from './gitTypes'
import type { GitUI } from './GitView'

function RefSelect({ label, value, onChange, ui }: { label: string; value: string; onChange: (v: string) => void; ui: GitUI }) {
  const local = ui.overview.branches.filter(b => !b.remote)
  const remote = ui.overview.branches.filter(b => b.remote)
  const known = ui.overview.branches.some(b => b.name === value) || ui.overview.tags.some(t => t.name === value)
  return (
    <label className="gcmp-ref">
      <span>{label}</span>
      <select className="git-select" value={value} onChange={e => onChange(e.target.value)}>
        {!known && value && <option value={value}>{value}</option>}
        <optgroup label="Branches">{local.map(b => <option key={b.name} value={b.name}>{b.name}</option>)}</optgroup>
        {remote.length > 0 && <optgroup label="Remote">{remote.map(b => <option key={b.name} value={b.name}>{b.name}</option>)}</optgroup>}
        {ui.overview.tags.length > 0 && <optgroup label="Tags">{ui.overview.tags.map(t => <option key={t.name} value={t.name}>{t.name}</option>)}</optgroup>}
      </select>
    </label>
  )
}

function CompareCommit({ c, onOpen }: { c: GitCommit; onOpen: (sha: string) => void }) {
  return (
    <button className="gcmp-commit" onClick={() => onOpen(c.sha)} title="Open in History">
      <Avatar name={c.author} size={18} />
      <span className="gcmp-commit__text">
        <span className="gcmp-commit__subject">{c.refs.map(r => <RefChip key={r.name} r={r} />)}{c.subject}</span>
        <span className="gcmp-commit__meta">{c.author} · <span title={fullDate(c.at)}>{ago(c.at)}</span> · <span className="git-mono">{shortSha(c.sha)}</span></span>
      </span>
      <Stat added={c.added} removed={c.removed} />
    </button>
  )
}

// Two refs side by side: how far apart they are, whether they merge, and everything that differs.
export function ComparePane({ ui, base, head, onChange, onOpenCommit }: {
  ui: GitUI
  base: string
  head: string
  onChange: (base: string, head: string) => void
  onOpenCommit: (sha: string) => void
}) {
  const [cmp, setCmp] = useState<GitCompare | null>(null)
  const [error, setError] = useState('')
  const [tab, setTab] = useState<'files' | 'commits'>('files')
  const [file, setFile] = useState<GitFileStat | null>(null)

  const effectiveBase = base || ui.overview.main
  const effectiveHead = head || ui.overview.branch || ui.overview.head

  useEffect(() => {
    if (!effectiveBase || !effectiveHead) return
    let cancelled = false
    setError('')
    gitCall<GitCompare>(ui.ctx, 'compare', { base: effectiveBase, head: effectiveHead })
      .then(c => {
        if (cancelled) return
        setCmp(c)
        setFile(prev => (prev && c.files.some(f => f.path === prev.path) ? prev : c.files[0] ?? null))
      })
      .catch(e => { if (!cancelled) { setError(errorText(e)); setCmp(null) } })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [effectiveBase, effectiveHead, ui.refsKey, ui.ctx.target?.repo, ui.ctx.worktree])

  const request: DiffRequest | null = useMemo(
    () => (file && cmp ? { mode: 'range', base: cmp.base, head: cmp.head, path: file.path, orig_path: file.orig_path } : null),
    [file, cmp],
  )
  const same = effectiveBase === effectiveHead
  const current = ui.overview.branch
  const canMerge = cmp && cmp.ahead > 0 && !cmp.conflicts && current === cmp.base && !ui.busy

  return (
    <div className="gcmp">
      <header className="gcmp__bar">
        <RefSelect label="Base" value={effectiveBase} onChange={v => onChange(v, effectiveHead)} ui={ui} />
        <button className="git-icon-btn" title="Swap" aria-label="Swap base and compared branch" onClick={() => onChange(effectiveHead, effectiveBase)}><IconArrowsExchange size={16} /></button>
        <RefSelect label="Compare" value={effectiveHead} onChange={v => onChange(effectiveBase, v)} ui={ui} />
        {cmp && !same && (
          <div className="gcmp__summary">
            <span title={`${cmp.ahead} commits in ${cmp.head} that are not in ${cmp.base}`}><b>{cmp.ahead}</b> ahead</span>
            <span title={`${cmp.behind} commits in ${cmp.base} that are not in ${cmp.head}`}><b>{cmp.behind}</b> behind</span>
            {cmp.merge_base && <span>from <button className="git-link git-mono" onClick={() => onOpenCommit(cmp.merge_base)}>{shortSha(cmp.merge_base)}</button></span>}
            {cmp.ahead > 0 && (cmp.conflicts
              ? <span className="gcmp__state gcmp__state--bad" title={cmp.conflict_files.join('\n')}><IconAlertTriangle size={13} />Conflicts in {cmp.conflict_files.length} {cmp.conflict_files.length === 1 ? 'file' : 'files'}</span>
              : <span className="gcmp__state gcmp__state--ok"><IconCircleCheck size={13} />Merges cleanly</span>)}
          </div>
        )}
        {cmp && cmp.ahead > 0 && (
          <button
            className="git-btn git-btn--primary gcmp__merge"
            disabled={!canMerge}
            title={current !== cmp.base ? `Check out ${cmp.base} to merge into it` : cmp.conflicts ? 'Git would stop on the conflicts: merge from the branch list to resolve them here' : undefined}
            onClick={() => {
              if (confirm(`Merge ${cmp.head} into ${cmp.base}?`)) void ui.act('merge', { ref: cmp.head, no_ff: true })
            }}
          ><IconGitMerge size={14} />Merge into {cmp.base}</button>
        )}
      </header>
      {error && <div className="git-msg git-msg--bad">{error}</div>}
      {same && <div className="git-empty git-empty--fill"><IconGitBranch size={18} />Choose two different branches to compare them.</div>}
      {!same && cmp && (
        <div className="gcmp__body">
          <section className="gcmp__list">
            <div className="git-tabs" role="tablist">
              <button role="tab" aria-selected={tab === 'files'} className={`git-tab${tab === 'files' ? ' git-tab--on' : ''}`} onClick={() => setTab('files')}>
                Files <span className="git-count">{cmp.files_total}</span>
              </button>
              <button role="tab" aria-selected={tab === 'commits'} className={`git-tab${tab === 'commits' ? ' git-tab--on' : ''}`} onClick={() => setTab('commits')}>
                Commits <span className="git-count">{cmp.ahead + cmp.behind}</span>
              </button>
              <span className="git-tabs__end"><Stat added={cmp.added} removed={cmp.removed} /></span>
            </div>
            <div className="gcmp__scroll">
              {tab === 'files' && (
                cmp.files.length === 0
                  ? <div className="git-empty">{cmp.head} adds nothing that {cmp.base} does not have.</div>
                  : cmp.files.map(f => {
                    const conflicted = cmp.conflict_files.includes(f.path)
                    return (
                      <div key={f.path} className={conflicted ? 'gcmp__conflicted' : undefined} title={conflicted ? 'Conflicts with the base' : undefined}>
                        <FileRow file={f} active={file?.path === f.path} onClick={() => setFile(f)} />
                      </div>
                    )
                  })
              )}
              {tab === 'commits' && <>
                {cmp.commits.length > 0 && <div className="git-section-head"><span>Only in {cmp.head}</span></div>}
                {cmp.commits.map(c => <CompareCommit key={c.sha} c={c} onOpen={onOpenCommit} />)}
                {cmp.base_commits.length > 0 && <div className="git-section-head"><span>Only in {cmp.base}</span></div>}
                {cmp.base_commits.map(c => <CompareCommit key={c.sha} c={c} onOpen={onOpenCommit} />)}
                {cmp.commits.length + cmp.base_commits.length === 0 && <div className="git-empty">Both point at the same commit.</div>}
              </>}
            </div>
          </section>
          <section className="gcmp__diff"><DiffView ctx={ui.ctx} request={request} /></section>
        </div>
      )}
    </div>
  )
}
