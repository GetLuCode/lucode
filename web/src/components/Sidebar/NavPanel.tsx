import { useRef, useState } from 'react'
import type { CSSProperties } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import { useStore } from '../../store'
import type { Project, Session } from '../../types'
import { IconEdit, IconTrash, IconSearch, IconLayoutSidebarLeftCollapse, IconLayoutSidebarLeftExpand, IconAdjustments, IconChartBar, IconLogout, IconDownload, IconUpload, IconCpu, IconRobot, IconLayoutKanban, IconHistory } from '@tabler/icons-react'
import { WorkspacePicker } from './WorkspacePicker'
import { SpacesList } from './SpacesList'
import { SettingsModal } from '../Settings/SettingsModal'
import { TaskManagerPanel } from '../TaskManager/TaskManagerPanel'
import { useAuthStore } from '../../store/auth'
import * as api from '../../api'

const PROJECT_COLORS = [
  '#7c8cff', '#4ade80', '#fb923c', '#f87171', '#a78bfa',
  '#38bdf8', '#facc15', '#f472b6', '#34d399', '#60a5fa',
]

function getProjectColor(name: string): string {
  let hash = 0
  for (let i = 0; i < name.length; i++) hash = name.charCodeAt(i) + ((hash << 5) - hash)
  return PROJECT_COLORS[Math.abs(hash) % PROJECT_COLORS.length]
}

function getProjectInitials(name: string): string {
  const words = name.trim().split(/[\s\-_.]+/).filter(Boolean)
  if (words.length >= 2) return (words[0][0] + words[1][0]).toUpperCase()
  return name.slice(0, 2).toUpperCase()
}

interface NavPanelProps {
  onOpenSearch?: () => void
}

export function NavPanel({ onOpenSearch }: NavPanelProps) {
  const navigate = useNavigate()
  const location = useLocation()
  const projects = useStore(s => s.projects)
  const sessions = useStore(s => s.sessions)
  const activeProjectId = useStore(s => s.activeProjectId)
  const activeSessionId = useStore(s => s.activeSessionId)
  const setActiveProject = useStore(s => s.setActiveProject)
  const setActiveSession = useStore(s => s.setActiveSession)
  const setPendingSession = useStore(s => s.setPendingSession)
  const createProject = useStore(s => s.createProject)
  const deleteProject = useStore(s => s.deleteProject)
  const renameProject = useStore(s => s.renameProject)
  const deleteSession = useStore(s => s.deleteSession)
  const renameSession = useStore(s => s.renameSession)
  const importSession = useStore(s => s.importSession)
  const sessionStatus = useStore(s => s.sessionStatus)
  const importRef = useRef<HTMLInputElement>(null)
  const importingProjectId = useRef<string | null>(null)

  const [collapsed, setCollapsed] = useState(false)
  const [showWorkspacePicker, setShowWorkspacePicker] = useState(false)
  const [showSettings, setShowSettings] = useState(false)
  const [showTaskManager, setShowTaskManager] = useState(false)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editingName, setEditingName] = useState('')
  const [editingType, setEditingType] = useState<'project' | 'session' | null>(null)
  const [activeProjectMenu, setActiveProjectMenu] = useState<string | null>(null)
  const [activeSessionMenu, setActiveSessionMenu] = useState<string | null>(null)

  const activeProject = projects.find(p => p.id === activeProjectId) ?? null

  const handleSelectProject = async (id: string) => {
    await setActiveProject(id)
    navigate(`/projects/${id}`)
  }

  const handleWorkspacePickerConfirm = async (name: string, path: string) => {
    const p = await createProject({ name, work_dirs: [path] })
    await setActiveProject(p.id)
    navigate(`/projects/${p.id}`)
  }

  const handleNewSession = (proj: Project) => {
    const model = proj.default_model || 'haiku'
    setPendingSession(proj.id, model)
  }

  const handleStartEditProject = (proj: Project, e: React.MouseEvent) => {
    e.stopPropagation()
    setEditingId(proj.id)
    setEditingName(proj.name)
    setEditingType('project')
    setActiveProjectMenu(null)
  }

  const handleStartEditSession = (sess: Session, e: React.MouseEvent) => {
    e.stopPropagation()
    setEditingId(sess.id)
    setEditingName(sess.name)
    setEditingType('session')
    setActiveSessionMenu(null)
  }

  const handleSaveEdit = async () => {
    if (editingId && editingName.trim()) {
      if (editingType === 'project') {
        await renameProject(editingId, editingName.trim())
      } else if (editingType === 'session') {
        await renameSession(editingId, editingName.trim())
      }
    }
    setEditingId(null)
    setEditingName('')
    setEditingType(null)
  }

  const statusDot = (sess: Session) => {
    const s = sessionStatus[sess.id] ?? sess.status
    if (s === 'running') return '●'
    if (s === 'waiting_hook') return '◆'
    if (s === 'error') return '✕'
    return '○'
  }

  const statusClass = (sess: Session) => {
    const s = sessionStatus[sess.id] ?? sess.status
    return `status-dot status-dot--${s}`
  }

  const handleExport = async (e: React.MouseEvent, sess: Session) => {
    e.stopPropagation()
    setActiveSessionMenu(null)
    try {
      const blob = await api.exportSession(sess.id)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `${(sess.name || 'session').replace(/[^a-zA-Z0-9_-]/g, '_')}.json`
      a.click()
      URL.revokeObjectURL(url)
    } catch (err) {
      console.error('export failed', err)
    }
  }

  const handleImportClick = (e: React.MouseEvent, projectId: string) => {
    e.stopPropagation()
    setActiveProjectMenu(null)
    importingProjectId.current = projectId
    importRef.current?.click()
  }

  const handleImportFile = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    const pid = importingProjectId.current
    if (!file || !pid) return
    e.target.value = ''
    try {
      const data = JSON.parse(await file.text())
      const sess = await importSession(pid, data)
      navigate(`/projects/${pid}/sessions/${sess.id}`)
    } catch (err) {
      console.error('import failed', err)
    }
  }

  return (
    <aside className={`nav-panel${collapsed ? ' nav-panel--collapsed' : ''}`}>
      {!collapsed && (
        <div className="nav-panel__backdrop" onClick={() => setCollapsed(true)} />
      )}

      {/* icon rail */}
      <div className="nav-panel__rail">
        <div className="nav-panel__rail-logo">
          <img src="/logo-icon.svg" alt="LuCode" width="20" height="20" />
        </div>
        <button
          className="nav-panel__rail-btn"
          onClick={() => setCollapsed(v => !v)}
          title={collapsed ? 'Expand panel' : 'Collapse panel'}
        >
          {collapsed
            ? <IconLayoutSidebarLeftExpand size={17} />
            : <IconLayoutSidebarLeftCollapse size={17} />
          }
        </button>

        {/* project icon buttons */}
        <div className="nav-panel__rail-projects">
          {projects.map((p: Project) => {
            const color = getProjectColor(p.name)
            const isActive = activeProjectId === p.id
            return (
              <button
                key={p.id}
                className={`nav-panel__project-icon${isActive ? ' nav-panel__project-icon--active' : ''}`}
                style={{ '--project-color': color } as CSSProperties}
                onClick={() => handleSelectProject(p.id)}
                title={p.name}
              >
                {getProjectInitials(p.name)}
              </button>
            )
          })}
          <button
            className="nav-panel__rail-btn nav-panel__project-add"
            onClick={() => setShowWorkspacePicker(v => !v)}
            title="New project"
          >+</button>
        </div>

        <div className="nav-panel__rail-spacer" />
        <button
          className="nav-panel__rail-btn"
          onClick={onOpenSearch}
          title="Search (Ctrl+K)"
        >
          <IconSearch size={16} />
        </button>
        <button
          className="nav-panel__rail-btn"
          onClick={() => navigate('/usage')}
          title="Usage metrics"
        >
          <IconChartBar size={16} />
        </button>
        <button
          className="nav-panel__rail-btn"
          title="Task Manager"
          onClick={() => setShowTaskManager(true)}
        >
          <IconCpu size={16} />
        </button>
        <button
          className="nav-panel__rail-btn"
          title="Settings"
          onClick={() => setShowSettings(true)}
        >
          <IconAdjustments size={16} />
        </button>
        <SettingsModal isOpen={showSettings} onClose={() => setShowSettings(false)} />
        <TaskManagerPanel isOpen={showTaskManager} onClose={() => setShowTaskManager(false)} />
      </div>

      <div className="nav-panel__body">
        <div className="nav-panel__logo">
          <img src="/logo-text.svg" alt="LuCode" className="nav-panel__logo-img" />
        </div>

        <WorkspacePicker
          isOpen={showWorkspacePicker}
          onClose={() => setShowWorkspacePicker(false)}
          onConfirm={handleWorkspacePickerConfirm}
        />
        <input ref={importRef} type="file" accept=".json" style={{ display: 'none' }} onChange={handleImportFile} />

        <SpacesList />

        {/* section nav — only shown when a project is active */}
        {activeProject && (
          <div className="nav-panel__section-nav">
            <button
              className={`nav-panel__section-btn${!location.pathname.includes('/agents') && !location.pathname.includes('/kanban') && !location.pathname.includes('/checkpoints') ? ' nav-panel__section-btn--active' : ''}`}
              onClick={() => navigate(`/projects/${activeProject.id}`)}
              title="Sessions"
            >
              Sessions
            </button>
            <button
              className={`nav-panel__section-btn${location.pathname.includes('/agents') ? ' nav-panel__section-btn--active' : ''}`}
              onClick={() => navigate(`/projects/${activeProject.id}/agents`)}
              title="Agents"
            >
              <IconRobot size={12} />
              Agents
            </button>
            <button
              className={`nav-panel__section-btn${location.pathname.includes('/kanban') ? ' nav-panel__section-btn--active' : ''}`}
              onClick={() => navigate(`/projects/${activeProject.id}/kanban`)}
              title="Kanban"
            >
              <IconLayoutKanban size={12} />
              Kanban
            </button>
            <button
              className={`nav-panel__section-btn${location.pathname.includes('/checkpoints') ? ' nav-panel__section-btn--active' : ''}`}
              onClick={() => navigate(`/projects/${activeProject.id}/checkpoints`)}
              title="Checkpoints"
            >
              <IconHistory size={12} />
              Checkpoints
            </button>
          </div>
        )}

        {/* active project sessions */}
        {activeProject && !location.pathname.includes('/agents') ? (
          <>
            <div className="nav-panel__header">
              {editingId === activeProject.id && editingType === 'project' ? (
                <input
                  className="input nav-tree__edit-input"
                  value={editingName}
                  onChange={e => setEditingName(e.target.value)}
                  onKeyDown={e => {
                    e.stopPropagation()
                    if (e.key === 'Enter') handleSaveEdit()
                    if (e.key === 'Escape') { setEditingId(null); setEditingName(''); setEditingType(null) }
                  }}
                  onBlur={handleSaveEdit}
                  autoFocus
                />
              ) : (
                <span className="nav-panel__label">{activeProject.name}</span>
              )}
              <button
                className="icon-btn"
                onClick={() => handleNewSession(activeProject)}
                title="New session"
              >+</button>
              <div className="nav-tree__menu-container">
                <button
                  className="icon-btn nav-tree__action-btn"
                  onClick={e => { e.stopPropagation(); setActiveProjectMenu(activeProjectMenu === activeProject.id ? null : activeProject.id) }}
                  title="Project menu"
                >⋯</button>
                {activeProjectMenu === activeProject.id && (
                  <div className="nav-tree__dropdown">
                    <button
                      className="nav-tree__menu-item"
                      onClick={e => handleStartEditProject(activeProject, e)}
                    >
                      <IconEdit size={14} />
                      <span>Rename</span>
                    </button>
                    <button
                      className="nav-tree__menu-item"
                      onClick={e => handleImportClick(e, activeProject.id)}
                    >
                      <IconUpload size={14} />
                      <span>Import session</span>
                    </button>
                    <button
                      className="nav-tree__menu-item nav-tree__menu-item--danger"
                      onClick={e => { e.stopPropagation(); deleteProject(activeProject.id); setActiveProjectMenu(null) }}
                    >
                      <IconTrash size={14} />
                      <span>Delete project</span>
                    </button>
                  </div>
                )}
              </div>
            </div>

            <ul className="nav-tree">
              {sessions.length === 0 ? (
                <li className="nav-tree__session nav-tree__session--empty">
                  <span>No sessions yet</span>
                </li>
              ) : (
                sessions.map((s: Session) => (
                  <li
                    key={s.id}
                    className={`nav-tree__session${activeSessionId === s.id ? ' nav-tree__session--active' : ''}`}
                    onClick={() => {
                      setActiveSession(s.id)
                      navigate(`/projects/${activeProjectId}/sessions/${s.id}`)
                    }}
                  >
                    <span className={statusClass(s)}>{statusDot(s)}</span>
                    <div className="nav-tree__session-info">
                      {editingId === s.id && editingType === 'session' ? (
                        <input
                          className="input session-list__edit-input"
                          value={editingName}
                          onChange={e => setEditingName(e.target.value)}
                          onKeyDown={e => {
                            e.stopPropagation()
                            if (e.key === 'Enter') handleSaveEdit()
                            if (e.key === 'Escape') { setEditingId(null); setEditingName(''); setEditingType(null) }
                          }}
                          onBlur={handleSaveEdit}
                          onClick={e => e.stopPropagation()}
                          autoFocus
                        />
                      ) : (
                        <>
                          <span className="nav-tree__session-name">{s.name || 'Untitled'}</span>
                          <span className="nav-tree__session-meta">{s.message_count} msgs</span>
                        </>
                      )}
                    </div>
                    <div className="nav-tree__menu-container">
                      <button
                        className="icon-btn nav-tree__action-btn"
                        onClick={e => { e.stopPropagation(); setActiveSessionMenu(activeSessionMenu === s.id ? null : s.id) }}
                        title="Session menu"
                      >⋯</button>
                      {activeSessionMenu === s.id && (
                        <div className="nav-tree__dropdown">
                          <button
                            className="nav-tree__menu-item"
                            onClick={e => { e.stopPropagation(); handleStartEditSession(s, e) }}
                          >
                            <IconEdit size={14} />
                            <span>Rename</span>
                          </button>
                          <button
                            className="nav-tree__menu-item"
                            onClick={e => handleExport(e, s)}
                          >
                            <IconDownload size={14} />
                            <span>Export</span>
                          </button>
                          <button
                            className="nav-tree__menu-item nav-tree__menu-item--danger"
                            onClick={e => { e.stopPropagation(); deleteSession(s.id); setActiveSessionMenu(null) }}
                          >
                            <IconTrash size={14} />
                            <span>Delete</span>
                          </button>
                        </div>
                      )}
                    </div>
                  </li>
                ))
              )}
            </ul>
          </>
        ) : null}

        {!activeProject && (
          <div className="nav-panel__empty">Select a project</div>
        )}

        <LogoutButton />
      </div>
    </aside>
  )
}

function LogoutButton() {
  const token = useAuthStore(s => s.token)
  const clearAuth = useAuthStore(s => s.clearAuth)

  if (!token) return null

  async function handleLogout() {
    try { await api.logout() } catch { /* ignore */ }
    clearAuth()
  }

  return (
    <button
      className="nav-logout-btn"
      onClick={handleLogout}
      title="Sign out"
    >
      <IconLogout size={14} />
      <span>Sign out</span>
    </button>
  )
}
