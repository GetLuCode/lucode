import { useEffect, useState } from 'react'
import { ago, errorText, fullDate, gitCall } from './gitApi'
import { FileRow, Sha } from './gitParts'
import { DiffView } from './DiffView'
import type { DiffRequest, GitFileStat, GitStash, GitStashFiles } from './gitTypes'
import type { GitUI } from './GitView'

// One stash: what it holds, and the three ways out of it.
export function StashPane({ ui, stash }: { ui: GitUI; stash: GitStash }) {
  const [files, setFiles] = useState<GitStashFiles | null>(null)
  const [error, setError] = useState('')
  const [picked, setPicked] = useState<{ file: GitFileStat; untracked: boolean } | null>(null)

  useEffect(() => {
    let cancelled = false
    setFiles(null)
    setError('')
    setPicked(null)
    gitCall<GitStashFiles>(ui.ctx, 'stash', { stash: stash.ref })
      .then(f => {
        if (cancelled) return
        setFiles(f)
        if (f.files[0]) setPicked({ file: f.files[0], untracked: false })
        else if (f.untracked[0]) setPicked({ file: f.untracked[0], untracked: true })
      })
      .catch(e => { if (!cancelled) setError(errorText(e)) })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [stash.sha, ui.ctx.target?.repo, ui.ctx.worktree])

  const request: DiffRequest | null = picked ? { mode: 'stash', sha: stash.ref, path: picked.file.path, untracked: picked.untracked } : null
  return (
    <div className="git-changes">
      <section className="gc-list" aria-label="Stash contents">
        <div className="gs-head">
          <h3 className="git-detail__subject">{stash.message}</h3>
          <div className="gs-meta">
            <span className="git-mono">{stash.ref}</span>
            <Sha sha={stash.sha} />
            <span className="git-dim" title={fullDate(stash.at)}>{ago(stash.at)} ago, by {stash.author}</span>
          </div>
          <div className="gs-actions">
            <button className="git-btn git-btn--primary" disabled={!!ui.busy} title="Apply it and delete the stash" onClick={() => void ui.act('stash_pop', { stash: stash.ref })}>Pop</button>
            <button className="git-btn" disabled={!!ui.busy} title="Apply it and keep the stash" onClick={() => void ui.act('stash_apply', { stash: stash.ref })}>Apply</button>
            <button className="git-btn git-btn--danger" disabled={!!ui.busy} onClick={() => {
              if (confirm(`Delete ${stash.ref}?\n\n"${stash.message}"\n\nIts changes cannot be recovered.`)) void ui.act('stash_drop', { stash: stash.ref })
            }}>Drop</button>
          </div>
        </div>
        <div className="gc-groups">
          {error && <div className="git-msg git-msg--bad">{error}</div>}
          {files && files.files.length > 0 && <div className="git-section-head"><span>Tracked <span className="git-count">{files.files.length}</span></span></div>}
          {files?.files.map(f => <FileRow key={f.path} file={f} active={picked?.file.path === f.path && !picked.untracked} onClick={() => setPicked({ file: f, untracked: false })} />)}
          {files && files.untracked.length > 0 && <div className="git-section-head"><span>Untracked <span className="git-count">{files.untracked.length}</span></span></div>}
          {files?.untracked.map(f => <FileRow key={f.path} file={f} active={picked?.file.path === f.path && picked.untracked} onClick={() => setPicked({ file: f, untracked: true })} />)}
        </div>
      </section>
      <section className="git-changes__diff" aria-label="Diff"><DiffView ctx={ui.ctx} request={request} /></section>
    </div>
  )
}
