import { useRef, useState } from 'react'
import { useLocation, useNavigate } from 'react-router-dom'
import { IconCpu, IconEdit, IconRobot, IconSearch, IconTrash, IconUpload } from '@tabler/icons-react'
import { useStore } from '../../store'
import { RunnersButton } from '../Sidebar/RunnersList'
import { ProjectEnvironmentModal } from '../Sidebar/ProjectEnvironmentModal'
import { ProjectEnvInfo } from './ProjectEnvInfo'
import { AlertsBell } from './AlertsBell'

// Top bar of a project: its name and menu, the Kanban | Running | Agent tabs, and the
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
  const runningCount = useStore(s => s.sessions.filter(x => x.card_id && (s.sessionStatus[x.id] ?? x.status) === 'running').length)

  const [menuOpen, setMenuOpen] = useState(false)
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState('')
  const [envOpen, setEnvOpen] = useState(false)

  const project = projects.find(p => p.id === activeProjectId)
  if (!project) return null

  const base = `/projects/${project.id}`
  const onAgents = pathname.includes('/agents')
  const onAgentTab = /\/agent\/?$/.test(pathname)
  const onKanban = pathname.includes('/kanban')
  // The running list and the chats opened from it share the Running tab.
  const onRunning = pathname.includes('/running') || pathname.includes('/sessions/')

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
        <ProjectEnvInfo project={project} />
        <div className="header-menu">
          <button className="header-icon-btn" onClick={() => setMenuOpen(v => !v)} title="Project menu" aria-label="Project menu">⋯</button>
          {menuOpen && (
            <>
              <div className="header-menu__backdrop" onClick={() => setMenuOpen(false)} />
              <div className="nav-tree__dropdown header-menu__dropdown header-menu__dropdown--left">
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
          className={`project-tab${onKanban ? ' project-tab--active' : ''}`}
          onClick={() => navigate(`${base}/kanban`)}
        >Kanban</button>
        <button
          className={`project-tab${onRunning ? ' project-tab--active' : ''}`}
          onClick={() => navigate(`${base}/running`)}
        >
          Running
          {runningCount > 0 && <span className="project-tab__badge" title={`${runningCount} agent(s) running`}>{runningCount}</span>}
        </button>
        <button
          className={`project-tab${onAgentTab ? ' project-tab--active' : ''}`}
          onClick={() => navigate(`${base}/agent`)}
        >Chat</button>
      </nav>

      <div className="project-header__actions">
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
