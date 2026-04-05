import { useEffect } from 'react'
import { useStore } from './store'
import { NavPanel } from './components/Sidebar/NavPanel'
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
      <NavPanel />
      <main className="main">
        <ChatView />
      </main>
    </div>
  )
}
