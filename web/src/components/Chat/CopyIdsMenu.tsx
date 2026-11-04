import { useState } from 'react'
import { IconCopy, IconCheck } from '@tabler/icons-react'
import { copyText } from '../../lib/copyText'

// Items of the chat header dropdown for an existing session: the model cannot be changed
// there, so it offers "Copy session id" and, when the session works on a board card, "Copy task id".
export function CopyIdItems({ sessionId, taskId }: { sessionId: string; taskId?: string | null }) {
  const [copied, setCopied] = useState<'session' | 'task' | ''>('')

  const copy = async (kind: 'session' | 'task', id: string) => {
    await copyText(id)
    setCopied(kind)
    setTimeout(() => setCopied(''), 900)
  }

  const item = (kind: 'session' | 'task', id: string) => (
    <button className="chat__header-dropdown-item" onClick={() => copy(kind, id)} title={id}>
      {copied === kind ? <IconCheck size={14} /> : <IconCopy size={14} />}
      <span>{copied === kind ? 'Copied' : `Copy ${kind} id`}</span>
    </button>
  )

  return (
    <>
      {item('session', sessionId)}
      {taskId && item('task', taskId)}
    </>
  )
}
