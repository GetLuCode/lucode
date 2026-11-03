import { useEffect, useState } from 'react'
import { useParams, useNavigate, useLocation } from 'react-router-dom'
import { useStore } from './store'
import { NavPanel } from './components/Sidebar/NavPanel'
import { ChatView } from './components/Chat/ChatView'
import { HookToastContainer } from './components/Toast/HookToastContainer'
import { SpotlightSearch } from './components/Search/SpotlightSearch'
import { TitleBar } from './components/TitleBar'
import { RawEventsModal } from './components/RawEvents/RawEventsModal'
import { initWSConnection } from './hooks/useWSConnection'
import { useGlobalWS } from './hooks/useGlobalWS'
import { AgentsView } from './components/Agents/AgentsView'
import { KanbanView } from './components/Kanban/KanbanView'
import { CheckpointsView } from './components/Checkpoints/CheckpointsView'

export default function App() {
  const [searchOpen, setSearchOpen] = useState(false)
  const loadProjects = useStore(s => s.loadProjects)
  const setActiveProject = useStore(s => s.setActiveProject)
  const setActiveSession = useStore(s => s.setActiveSession)
  const activeProjectId = useStore(s => s.activeProjectId)
  const activeSessionId = useStore(s => s.activeSessionId)
  const location = useLocation()

  const { projectId, sessionId } = useParams<{ projectId?: string; sessionId?: string }>()
  const navigate = useNavigate()

  // Global keyboard shortcut for search (Ctrl+K / Cmd+K)
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 'k') {
        e.preventDefault()
        setSearchOpen(v => !v)
      }
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [])

  // Connect to WS on app mount — async to resolve port via Wails binding if available
  useEffect(() => {
    initWSConnection()
  }, [])
  // Route events from every session into the store (tasks run in the background)
  useGlobalWS()

  // Load projects on mount
  useEffect(() => {
    loadProjects()
  }, [loadProjects])

  // Sync URL params to store
  useEffect(() => {
    console.log('[App] projectId from URL:', projectId)
    if (projectId) {
      setActiveProject(projectId)
    }
  }, [projectId, setActiveProject])

  useEffect(() => {
    // Only call setActiveSession when sessionId comes from URL navigation (reload/deep link),
    // not when the store itself just changed activeSessionId (which triggered the URL update).
    console.log('[App] sessionId from URL:', sessionId, 'activeSessionId in store:', activeSessionId)
    if (sessionId && sessionId !== activeSessionId) {
      setActiveSession(sessionId)
    }
  }, [sessionId, setActiveSession]) // eslint-disable-line react-hooks/exhaustive-deps

  // Sync store changes to URL — skip if already on a sub-route like /agents or /kanban
  useEffect(() => {
    if (location.pathname.includes('/agents') || location.pathname.includes('/kanban') || location.pathname.includes('/checkpoints')) return
    if (activeProjectId && activeSessionId) {
      navigate(`/projects/${activeProjectId}/sessions/${activeSessionId}`, { replace: true })
    } else if (activeProjectId) {
      // Opening the app (path "/") lands on the board; /projects/:id stays the sessions view.
      navigate(location.pathname === '/' ? `/projects/${activeProjectId}/kanban` : `/projects/${activeProjectId}`, { replace: true })
    }
  }, [activeProjectId, activeSessionId, navigate, location.pathname])

  const isAgentsRoute = location.pathname.includes('/agents')
  const isKanbanRoute = location.pathname.includes('/kanban')
  const isCheckpointsOpen = location.pathname.includes('/checkpoints')

  return (
    <div className="app">
      <TitleBar />
      <HookToastContainer />
      <SpotlightSearch isOpen={searchOpen} onClose={() => setSearchOpen(false)} />
      <RawEventsModal />
      <div className="app-content">
        <NavPanel onOpenSearch={() => setSearchOpen(true)} />
        <main className={`main${isCheckpointsOpen ? ' main--with-drawer' : ''}`}>
          {/* Agents and Kanban still replace the chat (full-page views). */}
          {isAgentsRoute ? <AgentsView /> : isKanbanRoute ? <KanbanView /> : <ChatView />}
        </main>
        <aside
          className={`checkpoints-drawer${isCheckpointsOpen ? ' checkpoints-drawer--open' : ''}`}
          aria-hidden={!isCheckpointsOpen}
        >
          <CheckpointsView onClose={() => navigate(-1)} />
        </aside>
      </div>
    </div>
  )
}
