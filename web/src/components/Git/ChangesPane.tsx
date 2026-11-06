import { useEffect, useMemo, useState } from 'react'
import { IconArchive, IconArrowBackUp, IconMinus, IconPlus, IconTrash } from '@tabler/icons-react'
import { CopyButton, StatusLetter, PathLabel, Stat } from './gitParts'
import { DiffView } from './DiffView'
import type { DiffRequest, GitChange } from './gitTypes'
import type { GitUI } from './GitView'

type Side = 'staged' | 'unstaged' | 'untracked' | 'conflict'
interface Picked { side: Side; path: string; orig?: string }

function diffFor(p: Picked | null): DiffRequest | null {
  if (!p) return null
  if (p.side === 'staged') return { mode: 'staged', path: p.path, orig_path: p.orig }
  if (p.side === 'untracked') return { mode: 'untracked', path: p.path }
  return { mode: 'worktree', path: p.path }
}

function ChangeRow({ c, side, active, ui, onPick }: { c: GitChange; side: Side; active: boolean; ui: GitUI; onPick: () => void }) {
  const status = side === 'staged' ? c.index : side === 'untracked' ? '?' : side === 'conflict' ? 'U' : c.worktree
  const added = side === 'staged' ? c.staged_add : c.unstaged_add
  const removed = side === 'staged' ? c.staged_del : c.unstaged_del
  const paths = [c.path]
  return (
    <div className={`gc-row${active ? ' gc-row--active' : ''}`}>
      <button className="gc-row__main" onClick={onPick}>
        <StatusLetter status={status} />
        <PathLabel path={c.path} orig={side === 'staged' ? c.orig_path : undefined} />
        <Stat added={added} removed={removed} />
      </button>
      <span className="gc-row__actions">
        <CopyButton text={c.path} label={`Copy the path of ${c.path}`} title="Copy path" className="git-icon-btn git-icon-btn--small" />
        {side === 'staged' && <button className="git-icon-btn git-icon-btn--small" title="Unstage" aria-label={`Unstage ${c.path}`} onClick={() => void ui.act('unstage', { paths })}><IconMinus size={13} /></button>}
        {side === 'unstaged' && <>
          <button className="git-icon-btn git-icon-btn--small git-icon-btn--danger" title="Discard these changes" aria-label={`Discard changes in ${c.path}`} onClick={() => {
            if (confirm(`Discard the unstaged changes in ${c.path}?\n\nThey cannot be recovered.`)) void ui.act('discard', { paths })
          }}><IconArrowBackUp size={13} /></button>
          <button className="git-icon-btn git-icon-btn--small git-icon-btn--danger" title="Delete the file (git rm)" aria-label={`Remove ${c.path} with git rm`} onClick={() => {
            if (confirm(`git rm ${c.path}?\n\nThe file is deleted from disk and the deletion is staged. Unsaved changes in it are lost.`)) void ui.act('rm', { paths })
          }}><IconTrash size={13} /></button>
          <button className="git-icon-btn git-icon-btn--small" title="Stage (git add)" aria-label={`Stage ${c.path}`} onClick={() => void ui.act('stage', { paths })}><IconPlus size={13} /></button>
        </>}
        {side === 'untracked' && <>
          <button className="git-icon-btn git-icon-btn--small git-icon-btn--danger" title="Delete this file" aria-label={`Delete ${c.path}`} onClick={() => {
            if (confirm(`Delete ${c.path}?\n\nGit does not track it, so it cannot be recovered.`)) void ui.act('clean', { paths })
          }}><IconTrash size={13} /></button>
          <button className="git-icon-btn git-icon-btn--small" title="Stage (git add)" aria-label={`Stage ${c.path}`} onClick={() => void ui.act('stage', { paths })}><IconPlus size={13} /></button>
        </>}
        {side === 'conflict' && <button className="git-icon-btn git-icon-btn--small" title="Mark as resolved (git add)" aria-label={`Mark ${c.path} as resolved`} onClick={() => void ui.act('stage', { paths })}><IconPlus size={13} /></button>}
      </span>
    </div>
  )
}

function Group({ title, count, action, children }: { title: string; count: number; action?: React.ReactNode; children: React.ReactNode }) {
  if (count === 0) return null
  return (
    <section className="gc-group">
      <header className="git-section-head">
        <span>{title} <span className="git-count">{count}</span></span>
        {action}
      </header>
      {children}
    </section>
  )
}

// Staged and unstaged work, side by side with its diff, and the commit box.
export function ChangesPane({ ui, statusKey }: { ui: GitUI; statusKey: string }) {
  const changes = ui.overview.changes
  const { conflict, staged, unstaged, untracked } = useMemo(() => ({
    conflict: changes.filter(c => c.conflict),
    staged: changes.filter(c => !c.conflict && c.index !== '.' && !c.untracked),
    unstaged: changes.filter(c => !c.conflict && c.worktree !== '.' && !c.untracked),
    untracked: changes.filter(c => c.untracked),
  }), [changes])
  const [picked, setPicked] = useState<Picked | null>(null)
  const [subject, setSubject] = useState('')
  const [body, setBody] = useState('')
  const [amend, setAmend] = useState(false)

  // the file was committed, staged or reverted: show the next one instead of an empty diff
  useEffect(() => {
    const has = (p: Picked) => {
      const list = { conflict, staged, unstaged, untracked }[p.side]
      return list.some(c => c.path === p.path)
    }
    if (picked && has(picked)) return
    const first = conflict[0] ? { side: 'conflict' as const, c: conflict[0] }
      : unstaged[0] ? { side: 'unstaged' as const, c: unstaged[0] }
      : staged[0] ? { side: 'staged' as const, c: staged[0] }
      : untracked[0] ? { side: 'untracked' as const, c: untracked[0] }
      : null
    setPicked(first ? { side: first.side, path: first.c.path, orig: first.c.orig_path } : null)
  }, [conflict, staged, unstaged, untracked, picked])

  const isPicked = (side: Side, c: GitChange) => picked?.side === side && picked.path === c.path
  const pick = (side: Side, c: GitChange) => setPicked({ side, path: c.path, orig: c.orig_path })
  const merging = ui.overview.state === 'merging' || ui.overview.state === 'cherry-picking' || ui.overview.state === 'reverting'
  const canCommit = !ui.busy && conflict.length === 0 && ((staged.length > 0 && (subject.trim() !== '' || merging)) || (amend && (subject.trim() !== '' || staged.length > 0)))
  const where = ui.overview.branch || 'detached HEAD'

  async function commit() {
    const message = [subject.trim(), body.trim()].filter(Boolean).join('\n\n')
    if (await ui.act('commit', { message, amend })) {
      setSubject('')
      setBody('')
      setAmend(false)
    }
  }

  const nothing = changes.length === 0
  return (
    <div className="git-changes">
      <section className="gc-list" aria-label="Working tree changes">
        <div className="gc-groups">
          {nothing && (
            <div className="git-empty git-empty--fill">
              <strong>The working tree is clean.</strong>
              <span>Nothing has changed since {ui.overview.unborn ? 'the repository was created' : 'the last commit'} on {where}.</span>
            </div>
          )}
          <Group title="Conflicts" count={conflict.length}>
            {conflict.map(c => <ChangeRow key={c.path} c={c} side="conflict" active={isPicked('conflict', c)} ui={ui} onPick={() => pick('conflict', c)} />)}
          </Group>
          <Group title="Staged" count={staged.length} action={<button className="git-text-btn" disabled={!!ui.busy} onClick={() => void ui.act('unstage')}>Unstage all</button>}>
            {staged.map(c => <ChangeRow key={c.path} c={c} side="staged" active={isPicked('staged', c)} ui={ui} onPick={() => pick('staged', c)} />)}
          </Group>
          <Group title="Not staged" count={unstaged.length} action={<button className="git-text-btn" disabled={!!ui.busy} onClick={() => void ui.act('stage', { paths: unstaged.map(c => c.path) })}>Stage all</button>}>
            {unstaged.map(c => <ChangeRow key={c.path} c={c} side="unstaged" active={isPicked('unstaged', c)} ui={ui} onPick={() => pick('unstaged', c)} />)}
          </Group>
          <Group title="Untracked" count={untracked.length} action={<button className="git-text-btn" disabled={!!ui.busy} onClick={() => void ui.act('stage', { paths: untracked.map(c => c.path) })}>Stage all</button>}>
            {untracked.map(c => <ChangeRow key={c.path} c={c} side="untracked" active={isPicked('untracked', c)} ui={ui} onPick={() => pick('untracked', c)} />)}
          </Group>
          {ui.overview.truncated && <div className="git-msg">Only the first 2000 changed files are listed.</div>}
        </div>
        <form className="gc-commit" onSubmit={e => { e.preventDefault(); if (canCommit) void commit() }}>
          <input
            className="input gc-commit__subject"
            value={subject}
            onChange={e => setSubject(e.target.value)}
            onKeyDown={e => e.stopPropagation()}
            placeholder={merging ? 'Commit message (git has one ready)' : 'Commit message'}
            aria-label="Commit message"
          />
          <textarea
            className="input gc-commit__body"
            value={body}
            onChange={e => setBody(e.target.value)}
            onKeyDown={e => e.stopPropagation()}
            placeholder="Description (optional)"
            aria-label="Commit description"
            rows={2}
          />
          <div className="gc-commit__row">
            <label className="git-check"><input type="checkbox" checked={amend} onChange={e => setAmend(e.target.checked)} /> Amend last commit</label>
            <button
              type="button"
              className="git-text-btn"
              disabled={nothing || !!ui.busy}
              title="Put these changes away (git stash)"
              onClick={async () => {
                const message = await ui.prompt({ title: 'Stash these changes', label: 'Name (optional)', action: 'Stash', optional: true })
                if (message !== null) await ui.act('stash_push', { message, include_untracked: true })
              }}
            ><IconArchive size={13} />Stash</button>
          </div>
          <button type="submit" className="git-btn git-btn--primary gc-commit__go" disabled={!canCommit}>
            {amend ? `Amend on ${where}` : staged.length ? `Commit ${staged.length} ${staged.length === 1 ? 'file' : 'files'} to ${where}` : `Commit to ${where}`}
          </button>
          {ui.overview.user_name && <div className="gc-commit__who">as {ui.overview.user_name}{ui.overview.user_email ? ` <${ui.overview.user_email}>` : ''}</div>}
        </form>
      </section>
      <section className="git-changes__diff" aria-label="Diff">
        <DiffView ctx={ui.ctx} request={diffFor(picked)} version={statusKey} />
      </section>
    </div>
  )
}
