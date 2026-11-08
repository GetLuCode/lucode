import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { useIsMobile } from '../../hooks/useIsMobile'
import { useLocation, useNavigate } from 'react-router-dom'
import { IconCpu, IconEdit, IconInfoCircle, IconRobot, IconSearch, IconTrash, IconUpload } from '@tabler/icons-react'
import { useStore } from '../../store'
import { RunnersButton } from '../Sidebar/RunnersList'
import { ProjectEnvironmentModal } from '../Sidebar/ProjectEnvironmentModal'
import { ProjectEnvInfo } from './ProjectEnvInfo'
import { AlertsBell } from './AlertsBell'
import { UsageMeters } from './UsageMeters'
import { usePlugins } from '../../store/plugins'
import { PluginIcon } from '../Plugins/PluginIcon'

// Top bar of a project: its name and menu, the Supervisor | Work | Running | Git | Automate tabs, and the
// Runners / Agents actions at the right.
export function ProjectHeader({ onOpenSearch }: { onOpenSearch?: () => void }) {
  const navigate = useNavigate()
  const { pathname } = useLocation()
  const projects = useStore(s => s.projects)
  const activeProjectId = useStore(s => s.activeProjectId)
  const deleteProject = useStore(s => s.deleteProject)
  const renameProject = useStore(s => s.renameProject)
  const importSession = useStore(s => s.importSession)
  const importRef = useRef<HTMLInputElement>(null)
  const pluginPages = usePlugins(s => s.ui.pages)
  const runningCount = useStore(s => s.sessions.filter(x => x.card_id && (s.sessionStatus[x.id] ?? x.status) === 'running').length)

  const isMobile = useIsMobile()
  // Looked up after mount: the slot is rendered by App in the same commit as this header.
  const [mobileSlot, setMobileSlot] = useState<HTMLElement | null>(null)
  useEffect(() => { setMobileSlot(isMobile ? document.getElementById('mobile-bar-actions') : null) }, [isMobile])
  const [menuOpen, setMenuOpen] = useState(false)
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState('')
  const [envOpen, setEnvOpen] = useState(false)
  const [infoOpen, setInfoOpen] = useState(false)

  const project = projects.find(p => p.id === activeProjectId)
  if (!project) return null
  // At phone width the actions live in the mobile bar (App.tsx), next to the logo.

  const base = `/projects/${project.id}`
  // a plugin's page id could be any word, also one of the ones below
  const onPlugin = pathname.includes(`${base}/plugins/`)
  const onAgents = !onPlugin && pathname.includes('/agents')
  const onAgentTab = !onPlugin && /\/agent\/?$/.test(pathname)
  const onKanban = !onPlugin && pathname.includes('/kanban')
  const onFlows = !onPlugin && pathname.includes('/flows')
  const onGit = !onPlugin && /\/git\/?$/.test(pathname)
  // The running list and the chats opened from it share the Running tab.
  const onRunning = !onPlugin && (pathname.includes('/running') || pathname.includes('/sessions/'))

  async function saveName() {
    if (name.trim() && name.trim() !== project!.name) await renameProject(project!.id, name.trim())
    setEditing(false)
  }

  // Leave the project's pages first: they must not render while it disappears.
  async function handleDelete() {
    if (!confirm(`Delete the project "${project!.name}"?\n\nIts tasks, agents and sessions go with it. This cannot be undone.`)) return
    const id = project!.id
    navigate('/', { replace: true })
    await deleteProject(id)
  }

  async function handleImportFile(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0]
    e.target.value = ''
    if (!file) return
    try {
      const sess = await importSession(project!.id, JSON.parse(await file.text()))
      navigate(`${base}/sessions/${sess.id}`)
    } catch (err) {
      console.error('import failed', err)
    }
  }

  const actions = (
      <div className="project-header__actions">
        <UsageMeters />
        <button className="header-icon-btn" onClick={onOpenSearch} title="Search (Ctrl+K)" aria-label="Search">
          <IconSearch size={16} />
        </button>
        <AlertsBell projectId={project.id} />
        <RunnersButton />
        <button
          className={`header-icon-btn${onAgents ? ' header-icon-btn--active' : ''}`}
          onClick={() => navigate(`${base}/agents`)}
          title="Agents"
          aria-label="Agents"
        >
          <IconRobot size={16} />
        </button>
      </div>
  )

  return (
    <header className="project-header">
      <div className="project-header__project">
        {editing ? (
          <input
            className="input project-header__name-input"
            value={name}
            onChange={e => setName(e.target.value)}
            onKeyDown={e => {
              e.stopPropagation()
              if (e.key === 'Enter') saveName()
              if (e.key === 'Escape') setEditing(false)
            }}
            onBlur={saveName}
            autoFocus
          />
        ) : (
          <span className="project-header__name" title={project.name}>{project.name}</span>
        )}
        <div className="header-menu">
          <button className="header-icon-btn" onClick={() => setMenuOpen(v => !v)} title="Project menu" aria-label="Project menu">⋯</button>
          {menuOpen && (
            <>
              <div className="header-menu__backdrop" onClick={() => setMenuOpen(false)} />
              <div className="nav-tree__dropdown header-menu__dropdown header-menu__dropdown--left">
                <button className="nav-tree__menu-item" onClick={() => { setInfoOpen(true); setMenuOpen(false) }}>
                  <IconInfoCircle size={14} /><span>Info</span>
                </button>
                <button className="nav-tree__menu-item" onClick={() => { setName(project.name); setEditing(true); setMenuOpen(false) }}>
                  <IconEdit size={14} /><span>Rename</span>
                </button>
                <button className="nav-tree__menu-item" onClick={() => { setEnvOpen(true); setMenuOpen(false) }}>
                  <IconCpu size={14} /><span>Environment…</span>
                </button>
                <button className="nav-tree__menu-item" onClick={() => { importRef.current?.click(); setMenuOpen(false) }}>
                  <IconUpload size={14} /><span>Import session</span>
                </button>
                <button
                  className="nav-tree__menu-item nav-tree__menu-item--danger"
                  onClick={() => { setMenuOpen(false); handleDelete() }}
                >
                  <IconTrash size={14} /><span>Delete project</span>
                </button>
              </div>
            </>
          )}
        </div>
        <input ref={importRef} type="file" accept=".json" style={{ display: 'none' }} onChange={handleImportFile} />
      </div>

      <nav className="project-tabs" aria-label="Project sections">
        <button
          className={`project-tab${onAgentTab ? ' project-tab--active' : ''}`}
          onClick={() => navigate(`${base}/agent`)}
        >Supervisor</button>
        <button
          className={`project-tab${onKanban ? ' project-tab--active' : ''}`}
          onClick={() => navigate(`${base}/kanban`)}
        >Work</button>
        <button
          className={`project-tab${onRunning ? ' project-tab--active' : ''}`}
          onClick={() => navigate(`${base}/running`)}
        >
          Running
          {runningCount > 0 && <span className="project-tab__badge" title={`${runningCount} agent(s) running`}>{runningCount}</span>}
        </button>
        <button
          className={`project-tab${onGit ? ' project-tab--active' : ''}`}
          onClick={() => navigate(`${base}/git`)}
        >Git</button>
        <button
          className={`project-tab${onFlows ? ' project-tab--active' : ''}`}
          onClick={() => navigate(`${base}/flows`)}
        >Automate</button>
        {pluginPages.map(pg => {
          const to = `${base}/plugins/${encodeURIComponent(pg.pluginId)}/${encodeURIComponent(pg.id)}`
          return (
            <button
              key={`${pg.pluginId}/${pg.id}`}
              className={`project-tab project-tab--plugin${pathname === to ? ' project-tab--active' : ''}`}
              onClick={() => navigate(to)}
            >
              <PluginIcon pluginId={pg.pluginId} path={pg.icon} size={13} fallback={<></>} />
              {pg.label}
            </button>
          )
        })}
      </nav>

      {isMobile ? (mobileSlot && createPortal(actions, mobileSlot)) : actions}

      {infoOpen && <ProjectEnvInfo project={project} onClose={() => setInfoOpen(false)} />}
      {envOpen && (
        <ProjectEnvironmentModal
          project={project}
          onClose={() => setEnvOpen(false)}
          onSaved={() => useStore.getState().loadProjects()}
        />
      )}
    </header>
  )
}
