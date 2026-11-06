import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { IconCheck, IconCopy } from '@tabler/icons-react'
import { backdropClose } from '../../lib/backdropClose'
import { copyText } from '../../lib/copyText'
import { authorHue, initials, shortSha } from './gitApi'
import type { GitFileStat, GitRef } from './gitTypes'

export function Avatar({ name, size = 18 }: { name: string; size?: number }) {
  const hue = authorHue(name)
  return (
    <span
      className="git-avatar"
      title={name}
      style={{ width: size, height: size, fontSize: size * 0.46, background: `hsl(${hue} 32% 24%)`, color: `hsl(${hue} 72% 80%)` }}
      aria-hidden
    >
      {initials(name)}
    </span>
  )
}

export function Sha({ sha, full }: { sha: string; full?: boolean }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      className="git-sha"
      title={`Copy ${sha}`}
      onClick={e => {
        e.stopPropagation()
        void copyText(sha)
        setCopied(true)
        setTimeout(() => setCopied(false), 1200)
      }}
    >
      {full ? sha : shortSha(sha)}
      {copied ? <IconCheck size={11} /> : <IconCopy size={11} className="git-sha__icon" />}
    </button>
  )
}

export function RefChip({ r }: { r: GitRef }) {
  const name = r.kind === 'tag' ? r.name : r.name
  return <span className={`git-ref git-ref--${r.kind}${r.head ? ' git-ref--current' : ''}`} title={`${r.kind}: ${r.name}`}>{name}</span>
}

export function Stat({ added, removed }: { added?: number; removed?: number }) {
  if (!added && !removed) return null
  return <span className="git-stat">{!!added && <i className="git-add">+{added}</i>}{!!removed && <i className="git-del">−{removed}</i>}</span>
}

const STATUS_NAME: Record<string, string> = { A: 'Added', M: 'Modified', D: 'Deleted', R: 'Renamed', C: 'Copied', T: 'Type changed', U: 'Conflict', '?': 'Untracked' }

export function StatusLetter({ status }: { status: string }) {
  return <span className={`git-status git-status--${status === '?' ? 'u' : status.toLowerCase()}`} title={STATUS_NAME[status] ?? status}>{status === '?' ? 'U' : status}</span>
}

export function PathLabel({ path, orig }: { path: string; orig?: string }) {
  const slash = path.lastIndexOf('/')
  return (
    <span className="git-path" title={orig ? `${orig} → ${path}` : path}>
      {orig && <span className="git-path__dir">{orig} → </span>}
      {slash >= 0 && <span className="git-path__dir">{path.slice(0, slash + 1)}</span>}
      <span className="git-path__name">{path.slice(slash + 1)}</span>
    </span>
  )
}

// Copies the text and says so for a moment.
export function CopyButton({ text, label, title, className = '', children }: { text: string; label: string; title?: string; className?: string; children?: ReactNode }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      type="button"
      className={className}
      title={title ?? label}
      aria-label={label}
      onClick={e => {
        e.stopPropagation()
        void copyText(text)
        setCopied(true)
        setTimeout(() => setCopied(false), 1200)
      }}
    >
      {copied ? <IconCheck size={13} /> : <IconCopy size={13} />}
      {children}
    </button>
  )
}

export function FileRow({ file, active, onClick }: { file: GitFileStat; active?: boolean; onClick: () => void }) {
  return (
    <div className={`git-file${active ? ' git-file--active' : ''}`}>
      <button className="git-file__main" onClick={onClick}>
        <StatusLetter status={file.status} />
        <PathLabel path={file.path} orig={file.orig_path} />
        {file.binary ? <span className="git-file__bin">bin</span> : <Stat added={file.added} removed={file.removed} />}
      </button>
      <CopyButton text={file.path} label={`Copy the path of ${file.path}`} title="Copy path" className="git-icon-btn git-icon-btn--small git-file__copy" />
    </div>
  )
}

export interface MenuItem {
  label: string
  icon?: ReactNode
  danger?: boolean
  disabled?: boolean
  onSelect: () => void
}

// A menu anchored to the button that opened it, or to the pointer for a context menu. It goes
// through a portal so the scrolling panels it opens from cannot clip it.
export function GitMenu({ anchor, point, items, onClose, header }: { anchor?: HTMLElement; point?: { x: number; y: number }; items: MenuItem[]; onClose: () => void; header?: ReactNode }) {
  const ref = useRef<HTMLDivElement>(null)
  const [pos, setPos] = useState<{ left: number; top: number } | null>(null)
  useLayoutEffect(() => {
    const r = anchor?.getBoundingClientRect()
    const left = r?.left ?? point?.x ?? 0
    const below = r?.bottom ?? point?.y ?? 0
    const above = r?.top ?? point?.y ?? 0
    const m = ref.current?.getBoundingClientRect()
    const w = m?.width ?? 200
    const h = m?.height ?? 200
    setPos({
      left: Math.max(8, Math.min(left, window.innerWidth - w - 8)),
      top: below + h + 8 > window.innerHeight ? Math.max(8, above - h - 4) : below + (r ? 4 : 0),
    })
  }, [anchor, point])
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])
  return createPortal(
    <>
      <div className="git-menu__backdrop" onClick={onClose} onContextMenu={e => { e.preventDefault(); onClose() }} />
      <div ref={ref} className="git-menu" role="menu" style={pos ? { left: pos.left, top: pos.top } : { visibility: 'hidden', left: 0, top: 0 }}>
        {header && <div className="git-menu__header">{header}</div>}
        {items.map(it => (
          <button
            key={it.label}
            role="menuitem"
            className={`git-menu__item${it.danger ? ' git-menu__item--danger' : ''}`}
            disabled={it.disabled}
            onClick={() => { onClose(); it.onSelect() }}
          >
            {it.icon}
            <span>{it.label}</span>
          </button>
        ))}
      </div>
    </>,
    document.body,
  )
}

export interface PromptOptions {
  title: string
  label: string
  initial?: string
  placeholder?: string
  action: string
  optional?: boolean
}

export function GitPrompt({ o, onDone }: { o: PromptOptions; onDone: (v: string | null) => void }) {
  const [value, setValue] = useState(o.initial ?? '')
  return (
    <div className="kmodal-backdrop" {...backdropClose(() => onDone(null))}>
      <form
        className="kmodal git-prompt"
        onSubmit={e => {
          e.preventDefault()
          if (o.optional || value.trim()) onDone(value.trim())
        }}
      >
        <div className="kmodal__header"><strong>{o.title}</strong></div>
        <div className="git-prompt__body">
          <label className="git-label" htmlFor="git-prompt-input">{o.label}</label>
          <input
            id="git-prompt-input"
            className="input"
            value={value}
            placeholder={o.placeholder}
            autoFocus
            onChange={e => setValue(e.target.value)}
            onKeyDown={e => { e.stopPropagation(); if (e.key === 'Escape') onDone(null) }}
          />
        </div>
        <div className="kmodal__footer">
          <button type="button" className="git-btn" onClick={() => onDone(null)}>Cancel</button>
          <button type="submit" className="git-btn git-btn--primary" disabled={!o.optional && !value.trim()}>{o.action}</button>
        </div>
      </form>
    </div>
  )
}
