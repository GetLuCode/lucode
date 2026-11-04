import { IconLayoutSidebarRightExpand } from '@tabler/icons-react'
import { useStore } from '../../store'
import { CacheBadge } from '../Chat/CacheBadge'
import { ModelIcon } from './ModelIcon'
import './OrchestratorPanel.css'

// Reopens the collapsed orchestrator panel. Drawn the same on every tab (except the chat):
// absolutely positioned, centered at the bottom of <main>, above the page content.
export function OrchestratorOpenButton() {
  const open = useStore(s => s.orchOpen)
  const setOpen = useStore(s => s.setOrchOpen)
  const cache = useStore(s => s.orchCache)
  const model = useStore(s => s.orchModel)
  if (open) return null
  const isLocalModel = model.startsWith('local:')
  return (
    <button className="orch-opener" onClick={() => setOpen(true)} title="Open the orchestrator chat">
      <ModelIcon model={model} size={16} />
      <IconLayoutSidebarRightExpand size={16} />
      <span>Orchestrator</span>
      {!isLocalModel && cache && cache.state !== 'none' && <CacheBadge cache={cache} />}
    </button>
  )
}
