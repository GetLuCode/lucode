import { useEffect } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useStore } from './store'
import { NavPanel } from './components/Sidebar/NavPanel'
import { ChatView } from './components/Chat/ChatView'
import { HookToastContainer } from './components/Toast/HookToastContainer'

export default function App() {
  const loadProjects = useStore(s => s.loadProjects)
  const setActiveProject = useStore(s => s.setActiveProject)
  const setActiveSession = useStore(s => s.setActiveSession)
  const activeProjectId = useStore(s => s.activeProjectId)
  const activeSessionId = useStore(s => s.activeSessionId)

  const { projectId, sessionId } = useParams<{ projectId?: string; sessionId?: string }>()
  const navigate = useNavigate()

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
    if (sessionId) {
      setActiveSession(sessionId)
    }
  }, [sessionId, setActiveSession])

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
      <NavPanel />
      <main className="main">
        <ChatView />
      </main>
    </div>
  )
}
