import { useState } from 'react'
import { IconDots, IconCopy, IconCheck } from '@tabler/icons-react'
import { copyText } from '../../lib/copyText'

// Three-dot menu in the chat header with a labelled "Copy ... id" item for the session and,
// when the session works on a board card, for the task.
export function CopyIdsMenu({ sessionId, taskId }: { sessionId: string; taskId?: string | null }) {
  const [open, setOpen] = useState(false)
  const [copied, setCopied] = useState<'session' | 'task' | ''>('')

  const copy = async (kind: 'session' | 'task', id: string) => {
    await copyText(id)
    setCopied(kind)
    setTimeout(() => { setCopied(''); setOpen(false) }, 900)
  }

  const item = (kind: 'session' | 'task', id: string) => (
    <button className="nav-tree__menu-item" role="menuitem" onClick={() => copy(kind, id)} title={id}>
      {copied === kind ? <IconCheck size={14} /> : <IconCopy size={14} />}
      <span>{copied === kind ? 'Copied' : `Copy ${kind} id`}</span>
    </button>
  )

  return (
    <div className="claude-proc header-menu">
      <button
        className="claude-proc__menu-btn"
        onClick={() => setOpen(v => !v)}
        title="More"
        aria-label="More actions"
        aria-haspopup="menu"
        aria-expanded={open}
      >
        <IconDots size={14} />
      </button>
      {open && (
        <>
          <div className="header-menu__backdrop" onClick={() => setOpen(false)} />
          <div className="nav-tree__dropdown header-menu__dropdown" role="menu">
            {item('session', sessionId)}
            {taskId && item('task', taskId)}
          </div>
        </>
      )}
    </div>
  )
}
