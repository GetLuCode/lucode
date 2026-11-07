import { IconLayoutSidebarRightExpand } from '@tabler/icons-react'
import { useStore } from '../../store'
import { CacheBadge } from '../Chat/CacheBadge'
import { ModelIcon } from './ModelIcon'
import { isPluginModel } from '../../lib/plugins'
import './OrchestratorPanel.css'

// Reopens the collapsed orchestrator panel. Drawn the same on every tab (except the chat):
// absolutely positioned, at the bottom right of <main>, above the page content.
export function OrchestratorOpenButton() {
  const open = useStore(s => s.orchOpen)
  const setOpen = useStore(s => s.setOrchOpen)
  const cache = useStore(s => s.orchCache)
  const model = useStore(s => s.orchModel)
  if (open) return null
  const answeredByPlugin = isPluginModel(model)
  return (
    <button className="orch-opener" onClick={() => setOpen(true)} title="Open the orchestrator chat">
      <ModelIcon model={model} size={16} />
      <IconLayoutSidebarRightExpand size={16} />
      <span>Supervisor</span>
      {!answeredByPlugin && cache && cache.state !== 'none' && <CacheBadge cache={cache} />}
    </button>
  )
}
