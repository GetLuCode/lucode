import { useEffect } from 'react'
import { useStore } from './store'
import { ProjectList } from './components/Sidebar/ProjectList'
import { SessionList } from './components/Sidebar/SessionList'
import { ChatView } from './components/Chat/ChatView'
import { HookToastContainer } from './components/Toast/HookToastContainer'

export default function App() {
  const loadProjects = useStore(s => s.loadProjects)

  useEffect(() => {
    loadProjects()
  }, [loadProjects])

  return (
    <div className="app">
      <HookToastContainer />
      <aside className="sidebar sidebar--projects">
        <ProjectList />
      </aside>
      <aside className="sidebar sidebar--sessions">
        <SessionList />
      </aside>
      <main className="main">
        <ChatView />
      </main>
    </div>
  )
}
