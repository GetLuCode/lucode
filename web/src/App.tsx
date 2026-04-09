import { useEffect, useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useStore } from './store'
import { NavPanel } from './components/Sidebar/NavPanel'
import { ChatView } from './components/Chat/ChatView'
import { HookToastContainer } from './components/Toast/HookToastContainer'
import { SpotlightSearch } from './components/Search/SpotlightSearch'
import { getWSConnection } from './hooks/useWSConnection'

export default function App() {
  const [searchOpen, setSearchOpen] = useState(false)
  const loadProjects = useStore(s => s.loadProjects)
  const setActiveProject = useStore(s => s.setActiveProject)
  const setActiveSession = useStore(s => s.setActiveSession)
  const activeProjectId = useStore(s => s.activeProjectId)
  const activeSessionId = useStore(s => s.activeSessionId)

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

  // Connect to WS on app mount for persistent global connection
  useEffect(() => {
    getWSConnection()
  }, [])

  // Load projects on mount
  useEffect(() => {
    loadProjects()
  }, [loadProjects])

  // Sync URL params to store
  useEffect(() => {
    if (projectId) {
      setActiveProject(projectId)
    }
  }, [projectId, setActiveProject])

  useEffect(() => {
    // Only call setActiveSession when sessionId comes from URL navigation (reload/deep link),
    // not when the store itself just changed activeSessionId (which triggered the URL update).
    if (sessionId && sessionId !== activeSessionId) {
      setActiveSession(sessionId)
    }
  }, [sessionId, setActiveSession]) // eslint-disable-line react-hooks/exhaustive-deps

  // Sync store changes to URL
  useEffect(() => {
    if (activeProjectId && activeSessionId) {
      navigate(`/projects/${activeProjectId}/sessions/${activeSessionId}`, { replace: true })
    } else if (activeProjectId) {
      navigate(`/projects/${activeProjectId}`, { replace: true })
    }
  }, [activeProjectId, activeSessionId, navigate])

  return (
    <div className="app">
      <HookToastContainer />
      <SpotlightSearch isOpen={searchOpen} onClose={() => setSearchOpen(false)} />
      <NavPanel onOpenSearch={() => setSearchOpen(true)} />
      <main className="main">
        <ChatView />
      </main>
    </div>
  )
}
