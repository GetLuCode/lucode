import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useStore } from './store'
import { SpotlightSearch } from './components/Search/SpotlightSearch'
import { NavRail } from './components/Sidebar/NavRail'
import { UsagePage } from './components/Usage/UsagePage'
import { initWSConnection } from './hooks/useWSConnection'

export default function UsageApp() {
  const [searchOpen, setSearchOpen] = useState(false)
  const loadProjects = useStore(s => s.loadProjects)
  const navigate = useNavigate()

  useEffect(() => { initWSConnection() }, [])
  useEffect(() => { loadProjects() }, [loadProjects])

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

  return (
    <div className="app">
      <SpotlightSearch isOpen={searchOpen} onClose={() => setSearchOpen(false)} />
      <NavRail
        activePage="usage"
        onOpenSearch={() => setSearchOpen(true)}
        onNavigate={page => navigate(page === 'home' ? '/' : `/${page}`)}
      />
      <main className="main">
        <UsagePage />
      </main>
    </div>
  )
}
