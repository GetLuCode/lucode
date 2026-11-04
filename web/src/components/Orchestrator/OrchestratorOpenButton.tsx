import { useStore } from '../../store'
import { CacheBadge } from '../Chat/CacheBadge'
import { ModelIcon } from './ModelIcon'

// Reopens the collapsed orchestrator panel. Shown at the bottom of the Kanban board,
// right after "Add list"; on the other pages the panel itself draws a tab at the edge.
export function OrchestratorOpenButton() {
  const open = useStore(s => s.orchOpen)
  const setOpen = useStore(s => s.setOrchOpen)
  const cache = useStore(s => s.orchCache)
  const model = useStore(s => s.orchModel)
  if (open) return null
  return (
    <button className="kanban-view__filter-btn" onClick={() => setOpen(true)} title="Open the orchestrator chat">
      <ModelIcon model={model} size={14} />
      Orchestrator
      {cache && cache.state !== 'none' && <CacheBadge cache={cache} />}
    </button>
  )
}
