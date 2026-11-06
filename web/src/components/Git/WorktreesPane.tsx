import { IconAlertTriangle, IconArrowRight, IconFolder, IconGitCompare, IconGitMerge, IconListDetails, IconLock, IconMessage, IconTrash } from '@tabler/icons-react'
import { ago, fullDate, shortSha } from './gitApi'
import type { GitMergeRef, GitTaskLink, GitWorktree, GitWorktrees } from './gitTypes'
import type { GitUI } from './GitView'

function MergeLine({ merge, onOpen }: { merge: GitMergeRef; onOpen: (sha: string) => void }) {
  if (merge.kind === 'contained') {
    return (
      <span className="gw-merge gw-merge--quiet" title="The tip of this branch is on the main line itself: it was fast-forwarded, or it has no commits of its own.">
        <IconGitMerge size={13} />In {merge.into} at{' '}
        {merge.sha && <button className="git-link git-mono" onClick={() => onOpen(merge.sha!)}>{shortSha(merge.sha)}</button>}, no merge commit
      </span>
    )
  }
  return (
    <span className="gw-merge">
      <IconGitMerge size={13} />
      {merge.kind === 'squash' ? 'Squashed into ' : 'Merged into '}<b>{merge.into}</b>
      {merge.sha && <> at <button className="git-link git-mono" onClick={() => onOpen(merge.sha!)} title={merge.subject}>{shortSha(merge.sha)}</button></>}
      {merge.author && <span className="git-dim"> by {merge.author}</span>}
      {merge.at ? <span className="git-dim" title={fullDate(merge.at)}> · {ago(merge.at)} ago</span> : null}
    </span>
  )
}

function TaskLine({ task, onSession }: { task: GitTaskLink; onSession: (id: string) => void }) {
  return (
    <span className="gw-task">
      <IconListDetails size={13} />
      <b className="gw-task__title">{task.title}</b>
      <span className={`gw-list gw-list--${task.list_kind}`}>{task.list}</span>
      {task.agent && <span className="git-dim">{task.agent}</span>}
      {task.running && <span className="gw-running">working now</span>}
      {task.session_id && (
        <button className="git-link gw-task__open" onClick={() => onSession(task.session_id!)}><IconMessage size={12} />Open chat</button>
      )}
    </span>
  )
}

function WorktreeRow({ wt, main, ui, onCompare, onOpenCommit, onSession, onUse }: {
  wt: GitWorktree
  main: string
  ui: GitUI
  onCompare: (branch: string) => void
  onOpenCommit: (sha: string) => void
  onSession: (id: string) => void
  onUse: (path: string) => void
}) {
  const inUse = (wt.main && !ui.ctx.worktree) || ui.ctx.worktree === wt.path
  const remove = () => {
    const why: string[] = []
    if (wt.dirty > 0) why.push(`It has ${wt.dirty} ${wt.dirty === 1 ? 'file' : 'files'} with uncommitted changes, which are lost.`)
    if (wt.locked) why.push(`It is locked${wt.lock_reason ? ` (${wt.lock_reason})` : ''}; the lock is removed too.`)
    if (wt.ahead > 0 && !wt.merge) why.push(`Its ${wt.ahead} ${wt.ahead === 1 ? 'commit stays' : 'commits stay'} on branch ${wt.branch}, not merged.`)
    if (confirm([`Remove the worktree ${wt.path}?`, ...why].join('\n\n'))) void ui.act('worktree_remove', { path: wt.path, force: wt.dirty > 0 })
  }
  return (
    <article className={`gw-row${inUse ? ' gw-row--inuse' : ''}`}>
      <div className="gw-row__head">
        <span className="gw-branch">{wt.branch || (wt.detached ? `detached ${shortSha(wt.head)}` : 'bare')}</span>
        {wt.main && <span className="git-tag">main checkout</span>}
        {inUse && <span className="git-tag git-tag--on">open here</span>}
        {wt.locked && <span className="git-tag" title={wt.lock_reason || 'Locked'}><IconLock size={11} />locked</span>}
        {wt.dirty > 0 && <span className="git-tag git-tag--warn">{wt.dirty} uncommitted</span>}
        {wt.missing && <span className="git-tag git-tag--bad"><IconAlertTriangle size={11} />directory gone</span>}
        {!wt.main && !wt.merge && wt.branch && (
          <span className="gw-ab" title={`Against ${main}`}>
            <span>↑{wt.ahead}</span><span>↓{wt.behind}</span><span className="git-dim"> {main}</span>
          </span>
        )}
        <span className="gw-row__actions">
          {!inUse && !wt.missing && <button className="git-text-btn" onClick={() => onUse(wt.main ? '' : wt.path)}><IconFolder size={13} />Open</button>}
          {!wt.main && wt.branch && <button className="git-text-btn" onClick={() => onCompare(wt.branch!)}><IconGitCompare size={13} />Compare</button>}
          {!wt.main && !inUse && (
            <button
              className="git-text-btn git-text-btn--danger"
              disabled={!!ui.busy || wt.task?.running}
              title={wt.task?.running ? 'Its agent is working in it' : 'git worktree remove'}
              onClick={remove}
            ><IconTrash size={13} />Remove</button>
          )}
        </span>
      </div>
      <div className="gw-path" title={wt.path}>{wt.path}</div>
      {wt.subject && (
        <div className="gw-last">
          <button className="git-link git-mono" onClick={() => onOpenCommit(wt.head)}>{shortSha(wt.head)}</button>
          <span className="gw-last__subject">{wt.subject}</span>
          {wt.author && <span className="git-dim">{wt.author}</span>}
          {wt.at ? <span className="git-dim" title={fullDate(wt.at)}>{ago(wt.at)}</span> : null}
        </div>
      )}
      {wt.task && <TaskLine task={wt.task} onSession={onSession} />}
      {!wt.main && !wt.task && <span className="gw-task gw-task--none">No task in this project worked here.</span>}
      {wt.merge && <MergeLine merge={wt.merge} onOpen={onOpenCommit} />}
      {!wt.main && !wt.merge && <span className="gw-merge gw-merge--quiet"><IconArrowRight size={13} />Not merged into {main} yet</span>}
    </article>
  )
}

// Every worktree with the task that owns it and where its work landed, then the tasks that were
// merged and cleaned up.
export function WorktreesPane({ ui, data, error, onCompare, onOpenCommit, onSession, onUse }: {
  ui: GitUI
  data: GitWorktrees | null
  error: string
  onCompare: (branch: string) => void
  onOpenCommit: (sha: string) => void
  onSession: (id: string) => void
  onUse: (path: string) => void
}) {
  return (
    <div className="gw">
      {error && <div className="git-msg git-msg--bad">{error}</div>}
      {!data && !error && <div className="git-skel git-skel--rows">{[70, 50, 80, 45, 66].map((w, i) => <span key={i} style={{ width: `${w}%` }} />)}</div>}
      {data && (
        <>
          <div className="git-section-head gw__head">
            <span>Worktrees <span className="git-count">{data.worktrees.length}</span></span>
            <button className="git-text-btn" disabled={!!ui.busy} title="git worktree prune: forget worktrees whose directory is gone" onClick={() => void ui.act('worktree_prune')}>Prune</button>
          </div>
          {data.worktrees.map(wt => (
            <WorktreeRow key={wt.path} wt={wt} main={data.main} ui={ui} onCompare={onCompare} onOpenCommit={onOpenCommit} onSession={onSession} onUse={onUse} />
          ))}
          {data.worktrees.length === 1 && <div className="git-empty">Only the main checkout. When an agent works in a worktree, it appears here with its task.</div>}
          {data.merged_tasks.length > 0 && (
            <>
              <div className="git-section-head gw__head">
                <span>Merged tasks, worktree removed <span className="git-count">{data.merged_tasks.length}</span></span>
              </div>
              {data.merged_tasks.map(t => (
                <article className="gw-row gw-row--past" key={t.card_id}>
                  <TaskLine task={t} onSession={onSession} />
                  <div className="gw-path">{t.branch}</div>
                  {t.merge
                    ? <MergeLine merge={t.merge} onOpen={onOpenCommit} />
                    : (
                      <span className="gw-merge gw-merge--quiet" title="The merge commit's message does not name this task, so the commit cannot be pointed at.">
                        <IconGitMerge size={13} />Merged into <b>{t.merged_into}</b>
                        {t.merged_at && <span className="git-dim"> · {new Date(t.merged_at).toLocaleString()}</span>}
                      </span>
                    )}
                </article>
              ))}
            </>
          )}
        </>
      )}
    </div>
  )
}
