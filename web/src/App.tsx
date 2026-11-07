import { useEffect, useState } from 'react'
import { useParams, useNavigate, useLocation } from 'react-router-dom'
import { useStore } from './store'
import * as api from './api'
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
import { ProjectHeader } from './components/Header/ProjectHeader'
import { ErrorBoundary } from './components/ErrorBoundary'
import { HomeView } from './components/Home/HomeView'
import { RunningView } from './components/Running/RunningView'
import { FlowsView } from './components/Flows/FlowsView'
import { GitView } from './components/Git/GitView'
import { RunnersView } from './components/Runners/RunnersView'
import { UsagePage } from './components/Usage/UsagePage'
import { SettingsPage } from './components/Settings/SettingsPage'
import { OrchestratorPanel } from './components/Orchestrator/OrchestratorPanel'
import { OrchestratorOpenButton } from './components/Orchestrator/OrchestratorOpenButton'
import { PluginHost } from './components/Plugins/PluginHost'
import { PluginPage } from './components/Plugins/PluginPage'
import { PluginsPage } from './components/Plugins/PluginsPage'

export default function App() {
  const [searchOpen, setSearchOpen] = useState(false)
  const [navOpen, setNavOpen] = useState(false) // left bar as an overlay (phone width only)
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

  // Deep links (reload, bookmark, a project or session that was deleted): the ids in
  // the URL are checked before they reach the store, so a stale URL falls back to a
  // page that exists instead of leaving a blank UI.
  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        await loadProjects()
        if (cancelled || !projectId) return
        if (!useStore.getState().projects.some(p => p.id === projectId)) {
          navigate('/', { replace: true }) // the project is gone
          return
        }
        // In-app navigation already selected it; only a reload or a deep link gets here unselected.
        if (useStore.getState().activeProjectId !== projectId) await setActiveProject(projectId)
      } catch (err) {
        console.error('[App] could not open project', projectId, err)
        if (!cancelled) navigate('/', { replace: true })
      }
    })()
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId])

  useEffect(() => {
    if (!projectId || !sessionId) return
    let cancelled = false
    ;(async () => {
      try {
        // Only a URL that was typed or reloaded needs this: clicking a session in the
        // app already made it the active one.
        if (sessionId === useStore.getState().activeSessionId) return
        const known = useStore.getState().sessions.some(s => s.id === sessionId)
        const exists = known || (await api.listSessions(projectId)).some(s => s.id === sessionId)
        if (cancelled) return
        if (!exists) {
          navigate(`/projects/${projectId}/running`, { replace: true }) // the session is gone
          return
        }
        await setActiveSession(sessionId)
      } catch (err) {
        console.error('[App] could not open session', sessionId, err)
        if (!cancelled) navigate(`/projects/${projectId}/running`, { replace: true })
      }
    })()
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId, sessionId])

  // Sync store changes to URL — skip if already on a sub-route like /agents or /kanban.
  // A bare /projects/:id lands on the Supervisor tab; "/" is the start page.
  useEffect(() => {
    const path = location.pathname
    if (path === '/') return // the start page
    if (['/agents', '/agent', '/kanban', '/running', '/flows', '/git', '/runners', '/checkpoints', '/usage', '/plugins', '/settings'].some(p => path.includes(p))) return
    if (!activeProjectId) return
    if (path.includes('/sessions/')) {
      if (activeSessionId) navigate(`/projects/${activeProjectId}/sessions/${activeSessionId}`, { replace: true })
      return // a deep link: wait for the store to pick the session up
    }
    navigate(`/projects/${activeProjectId}/agent`, { replace: true })
  }, [activeProjectId, activeSessionId, navigate, location.pathname])

  // Any navigation closes the overlay.
  useEffect(() => { setNavOpen(false) }, [location.pathname])

  const isSettings = location.pathname.startsWith('/settings')
  // account-wide pages: no project header, no supervisor panel
  const isUsage = location.pathname === '/usage' || isSettings
  const isHome = location.pathname === '/'
  // first: a plugin's page id could be any word, also one of the ones below
  const isPluginPage = /^\/projects\/[^/]+\/plugins\//.test(location.pathname)
  const isPluginsList = /^\/projects\/[^/]+\/plugins\/?$/.test(location.pathname) // installed plugins and install
  const isAgentsRoute = !isPluginPage && location.pathname.includes('/agents')
  const isKanbanRoute = !isPluginPage && location.pathname.includes('/kanban')
  const isRunningRoute = !isPluginPage && location.pathname.includes('/running')
  const isFlowsRoute = !isPluginPage && location.pathname.includes('/flows')
  const isGitRoute = !isPluginPage && /\/git\/?$/.test(location.pathname)
  const isRunnersRoute = !isPluginPage && location.pathname.includes('/runners')
  const isAgentTab = !isPluginPage && /\/agent\/?$/.test(location.pathname)
  const isCheckpointsOpen = !isPluginPage && location.pathname.includes('/checkpoints')

  return (
    <div className="app">
      <TitleBar />
      <HookToastContainer />
      <PluginHost />
      <SpotlightSearch isOpen={searchOpen} onClose={() => setSearchOpen(false)} />
      <RawEventsModal />
      <div className="mobile-bar">
        <button className="mobile-bar__logo" onClick={() => setNavOpen(v => !v)} aria-label="Menu" aria-expanded={navOpen}>
          <img src={new URL('../public/logo-icon.svg', import.meta.url).href} alt="LuCode" width="24" height="24" />
        </button>
        <div id="mobile-bar-actions" className="mobile-bar__actions" />
      </div>
      <div className={`app-content${isAgentTab ? ' app-content--agent' : ''}`}>
        <NavPanel mobileOpen={navOpen} onMobileClose={() => setNavOpen(false)} />
        <main className={`main${isCheckpointsOpen ? ' main--with-drawer' : ''}`}>
          {!isHome && !isUsage && <ProjectHeader onOpenSearch={() => setSearchOpen(true)} />}
          {/* Agents, Kanban and Running replace the chat (full-page views). */}
          <div className="main__content">
            {/* a render error stays inside the page and clears when the route changes */}
            <ErrorBoundary key={location.pathname}>
              {isHome ? <HomeView /> : isSettings ? <SettingsPage /> : isUsage ? <UsagePage /> : isPluginsList ? <PluginsPage /> : isPluginPage ? <PluginPage /> : isAgentsRoute ? <AgentsView /> : isKanbanRoute ? <KanbanView /> : isRunningRoute ? <RunningView /> : isFlowsRoute ? <FlowsView /> : isGitRoute ? <GitView /> : isRunnersRoute ? <RunnersView /> : isAgentTab ? null : <ChatView />}
            </ErrorBoundary>
          </div>
          {/* same button on every tab except the chat; the Agent tab already shows the panel */}
          {!isHome && !isUsage && !isAgentTab && (isAgentsRoute || isKanbanRoute || isRunningRoute || isFlowsRoute || isGitRoute || isPluginPage || isPluginsList) && <OrchestratorOpenButton />}
        </main>
        {!isHome && !isUsage && <OrchestratorPanel full={isAgentTab} />}
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
