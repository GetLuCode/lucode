import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useStore } from '../../store'
import type { Project, Session } from '../../types'
import { IconEdit, IconTrash, IconSearch, IconLayoutSidebarLeftCollapse, IconLayoutSidebarLeftExpand, IconAdjustments, IconChartBar } from '@tabler/icons-react'
import { WorkspacePicker } from './WorkspacePicker'
import { SettingsModal } from '../Settings/SettingsModal'
import { AccountFooter } from './AccountFooter'

interface NavPanelProps {
  onOpenSearch?: () => void
}

export function NavPanel({ onOpenSearch }: NavPanelProps) {
  const navigate = useNavigate()
  const projects = useStore(s => s.projects)
  // Determine if a Claude/Anthropic model is active (not Ollama)
  const activeModel = localStorage.getItem('pref_model') ?? 'haiku'
  const isClaudeModel = !activeModel.startsWith('ollama:')
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
  const sessionStatus = useStore(s => s.sessionStatus)

  const [collapsed, setCollapsed] = useState(false)
  const [expandedProjects, setExpandedProjects] = useState<Set<string>>(new Set())
  const [showWorkspacePicker, setShowWorkspacePicker] = useState(false)
  const [showSettings, setShowSettings] = useState(false)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editingName, setEditingName] = useState('')
  const [editingType, setEditingType] = useState<'project' | 'session' | null>(null)
  const [activeProjectMenu, setActiveProjectMenu] = useState<string | null>(null)
  const [activeSessionMenu, setActiveSessionMenu] = useState<string | null>(null)

  const toggleProject = (id: string) => {
    setExpandedProjects(prev => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const handleSelectProject = async (id: string) => {
    await setActiveProject(id)
    setExpandedProjects(prev => new Set([...prev, id]))
    navigate(`/projects/${id}`)
  }

  const handleWorkspacePickerConfirm = async (name: string, path: string) => {
    const p = await createProject({ name, work_dirs: [path] })
    await setActiveProject(p.id)
    setExpandedProjects(prev => new Set([...prev, p.id]))
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

  // Sessions are loaded for the active project only
  const getProjectSessions = (projectId: string): Session[] => {
    if (activeProjectId !== projectId) return []
    return sessions
  }

  return (
    <aside className={`nav-panel${collapsed ? ' nav-panel--collapsed' : ''}`}>
      {/* mobile backdrop — closes panel on outside click */}
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
        <div className="nav-panel__rail-spacer" />
        <button
          className="nav-panel__rail-btn"
          title="Settings"
          onClick={() => setShowSettings(true)}
        >
          <IconAdjustments size={16} />
        </button>
        <SettingsModal isOpen={showSettings} onClose={() => setShowSettings(false)} />
      </div>

      <div className="nav-panel__body">
        {/* logo */}
        <div className="nav-panel__logo">
          <img src="/logo-text.svg" alt="LuCode" className="nav-panel__logo-img" />
        </div>

        {/* header */}
        <div className="nav-panel__header">
          <span className="nav-panel__label">Workspace</span>
          <button
            className="icon-btn"
            onClick={() => setShowWorkspacePicker(v => !v)}
            title="New workspace"
          >+</button>
        </div>

        <WorkspacePicker
          isOpen={showWorkspacePicker}
          onClose={() => setShowWorkspacePicker(false)}
          onConfirm={handleWorkspacePickerConfirm}
        />

        {/* tree */}
        <ul className="nav-tree">
          {projects.map((p: Project) => {
            const isActive = activeProjectId === p.id
            const isExpanded = expandedProjects.has(p.id)
            const pSessions = getProjectSessions(p.id)

            return (
              <li key={p.id} className="nav-tree__project-group">
                {/* project row */}
                <div
                  className={`nav-tree__project${isActive ? ' nav-tree__project--active' : ''}`}
                  onClick={() => handleSelectProject(p.id)}
                >
                  <button
                    className="nav-tree__chevron"
                    onClick={e => { e.stopPropagation(); toggleProject(p.id) }}
                    title="Toggle sessions"
                  >
                    <span className={`nav-tree__chevron-icon${isExpanded ? ' nav-tree__chevron-icon--open' : ''}`}>▸</span>
                  </button>
                  <span className="nav-tree__project-dot" />
                  {editingId === p.id && editingType === 'project' ? (
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
                      onClick={e => e.stopPropagation()}
                      autoFocus
                    />
                  ) : (
                    <span className="nav-tree__project-name">{p.name}</span>
                  )}
                  <div className="nav-tree__actions">
                    {isActive && (
                      <button
                        className="icon-btn nav-tree__action-btn"
                        onClick={e => { e.stopPropagation(); handleNewSession(p) }}
                        title="New session"
                      >+</button>
                    )}
                    <div className="nav-tree__menu-container">
                      <button
                        className="icon-btn nav-tree__action-btn"
                        onClick={e => { e.stopPropagation(); setActiveProjectMenu(activeProjectMenu === p.id ? null : p.id) }}
                        title="Project menu"
                      >⋯</button>
                      {activeProjectMenu === p.id && (
                        <div className="nav-tree__dropdown">
                          <button
                            className="nav-tree__menu-item"
                            onClick={e => { e.stopPropagation(); handleStartEditProject(p, e) }}
                          >
                            <IconEdit size={14} />
                            <span>Rename</span>
                          </button>
                          <button
                            className="nav-tree__menu-item nav-tree__menu-item--danger"
                            onClick={e => { e.stopPropagation(); deleteProject(p.id); setActiveProjectMenu(null) }}
                          >
                            <IconTrash size={14} />
                            <span>Delete</span>
                          </button>
                        </div>
                      )}
                    </div>
                  </div>
                </div>

                {/* sessions subtree */}
                {isExpanded && isActive && (
                  <ul className="nav-tree__sessions">
                    {pSessions.length === 0 ? (
                      <li className="nav-tree__session nav-tree__session--empty">
                        <span>No sessions yet</span>
                      </li>
                    ) : (
                      pSessions.map((s: Session) => (
                        <li
                          key={s.id}
                          className={`nav-tree__session${activeSessionId === s.id ? ' nav-tree__session--active' : ''}`}
                          onClick={() => {
                            setActiveSession(s.id)
                            navigate(`/projects/${activeProjectId}/sessions/${s.id}`)
                          }}
                        >
                          <span className="nav-tree__connector" />
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
                                <span className="nav-tree__session-name">
                                  {s.name || 'Untitled'}
                                </span>
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
                )}
              </li>
            )
          })}
        </ul>

        <AccountFooter visible={isClaudeModel} />
      </div>
    </aside>
  )
}
