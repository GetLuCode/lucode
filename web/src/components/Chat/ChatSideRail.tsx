import { useNavigate, useLocation } from 'react-router-dom'
import { useStore } from '../../store'
import { IconHistory, IconFolder } from '@tabler/icons-react'

// Floating side rail on the right edge of the chat. Houses shortcuts to
// secondary panels (Checkpoints, File browser) so the section-nav in the
// sidebar can stay focused on the primary views.
export function ChatSideRail() {
  const navigate = useNavigate()
  const location = useLocation()
  const activeProjectId = useStore(s => s.activeProjectId)
  const fileBrowserOpen = useStore(s => s.fileBrowserOpen)
  const openFileBrowser = useStore(s => s.openFileBrowser)
  const closeFileBrowser = useStore(s => s.closeFileBrowser)
  const filePreviewOpen = useStore(s => s.filePreview !== null)

  // The rail is only meaningful inside a project.
  if (!activeProjectId) return null

  // Hide when the chat is replaced by a full-page view (Agents, Kanban).
  if (location.pathname.includes('/agents') || location.pathname.includes('/kanban')) {
    return null
  }

  // The rail is shown on the chat routes and on the checkpoints route so
  // the user can use the icons to close the panel or switch panels.
  const isInProject = location.pathname.includes('/sessions/') ||
    location.pathname.match(/\/projects\/[^/]+$/) !== null ||
    location.pathname.includes('/checkpoints')
  if (!isInProject) return null

  const checkpointsActive = location.pathname.includes('/checkpoints')

  const handleCheckpointsClick = () => {
    // Toggle: if we're already on the checkpoints route, go back to the chat.
    if (checkpointsActive) {
      navigate(-1)
    } else {
      // Close the file browser so only one panel is open at a time.
      if (fileBrowserOpen) closeFileBrowser()
      navigate(`/projects/${activeProjectId}/checkpoints`)
    }
  }

  const handleFileBrowserClick = () => {
    if (fileBrowserOpen) {
      closeFileBrowser()
    } else {
      // Close the checkpoints drawer so only one panel is open at a time.
      if (checkpointsActive) navigate(-1)
      openFileBrowser()
    }
  }

  // Visual dim when the preview is open, to keep it from competing with
  // the file preview's toolbar.
  const className = `chat-side-rail${filePreviewOpen ? ' chat-side-rail--dim' : ''}`

  return (
    <div className={className} role="toolbar" aria-orientation="vertical">
      <button
        className={`chat-side-rail__btn${checkpointsActive ? ' chat-side-rail__btn--active' : ''}`}
        onClick={handleCheckpointsClick}
        title="Checkpoints"
        aria-label="Checkpoints"
        aria-pressed={checkpointsActive}
      >
        <IconHistory size={18} />
      </button>
      <button
        className={`chat-side-rail__btn${fileBrowserOpen ? ' chat-side-rail__btn--active' : ''}`}
        onClick={handleFileBrowserClick}
        title="File browser"
        aria-label="File browser"
        aria-pressed={fileBrowserOpen}
      >
        <IconFolder size={18} />
      </button>
    </div>
  )
}
