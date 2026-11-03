import { useEffect, useState } from 'react'
import { useStore } from '../../store'
import * as api from '../../api'
import './TaskStatus.css'

// Where a task is on the board, by the kind of its list.
const KIND_LABEL: Record<string, string> = {
  backlog: 'Backlog', todo: 'To do', inprogress: 'In progress', done: 'Done', blocked: 'Blocked',
}

// The status of the task a session works on (the board list its card is in), shown by the
// session title. It follows the board: the card is read again whenever a task changes.
export function TaskStatus({ projectId, cardId }: { projectId: string; cardId: string }) {
  const boardVersion = useStore(s => s.boardVersion)
  const [status, setStatus] = useState<{ name: string; kind: string; title: string } | null>(null)

  useEffect(() => {
    let cancelled = false
    Promise.all([api.listKanbanLists(projectId), api.listKanbanCards(projectId)])
      .then(([lists, cards]) => {
        if (cancelled) return
        const card = cards.find(c => c.id === cardId)
        const list = card && lists.find(l => l.id === card.list_id)
        setStatus(card && list ? { name: list.name, kind: list.kind ?? '', title: card.title } : null)
      })
      .catch(() => { if (!cancelled) setStatus(null) })
    return () => { cancelled = true }
  }, [projectId, cardId, boardVersion])

  if (!status) return null
  return (
    <span className={`task-status task-status--${status.kind || 'custom'}`} title={`Task: ${status.title}`}>
      {status.name || KIND_LABEL[status.kind] || 'Task'}
    </span>
  )
}
