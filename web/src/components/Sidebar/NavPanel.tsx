import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useStore } from '../../store'
import type { Project, Session } from '../../types'

export function NavPanel() {
  const navigate = useNavigate()
  const projects = useStore(s => s.projects)
  const sessions = useStore(s => s.sessions)
  const activeProjectId = useStore(s => s.activeProjectId)
  const activeSessionId = useStore(s => s.activeSessionId)
  const setActiveProject = useStore(s => s.setActiveProject)
  const setActiveSession = useStore(s => s.setActiveSession)
  const setPendingSession = useStore(s => s.setPendingSession)
  const createProject = useStore(s => s.createProject)
  const deleteProject = useStore(s => s.deleteProject)
  const deleteSession = useStore(s => s.deleteSession)
  const renameSession = useStore(s => s.renameSession)
  const sessionStatus = useStore(s => s.sessionStatus)

  const [collapsed, setCollapsed] = useState(false)
  const [expandedProjects, setExpandedProjects] = useState<Set<string>>(new Set())
  const [showNewProject, setShowNewProject] = useState(false)
  const [newName, setNewName] = useState('')
  const [newDirs, setNewDirs] = useState('')
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editingName, setEditingName] = useState('')

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

  const handleCreateProject = async () => {
    if (!newName.trim()) return
    const dirs = newDirs.split('\n').map(d => d.trim()).filter(Boolean)
    const p = await createProject({ name: newName.trim(), work_dirs: dirs })
    await setActiveProject(p.id)
    setExpandedProjects(prev => new Set([...prev, p.id]))
    navigate(`/projects/${p.id}`)
    setShowNewProject(false)
    setNewName('')
    setNewDirs('')
  }

  const handleNewSession = (proj: Project) => {
    const model = proj.default_model || 'haiku'
    setPendingSession(proj.id, model)
  }

  const handleStartEdit = (sess: Session, e: React.MouseEvent) => {
    e.stopPropagation()
    setEditingId(sess.id)
    setEditingName(sess.name)
  }

  const handleSaveEdit = async () => {
    if (editingId && editingName.trim()) {
      await renameSession(editingId, editingName.trim())
    }
    setEditingId(null)
    setEditingName('')
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
      {/* collapse toggle rail */}
      <button
        className="nav-panel__rail"
        onClick={() => setCollapsed(v => !v)}
        title={collapsed ? 'Expand panel' : 'Collapse panel'}
      >
        <span className="nav-panel__rail-icon">{collapsed ? '›' : '‹'}</span>
      </button>

      <div className="nav-panel__body">
        {/* header */}
        <div className="nav-panel__header">
          <span className="nav-panel__label">Workspace</span>
          <button
            className="icon-btn"
            onClick={() => setShowNewProject(v => !v)}
            title="New project"
          >+</button>
        </div>

        {/* new project form */}
        {showNewProject && (
          <div className="nav-panel__new-project">
            <input
              className="input"
              placeholder="Project name"
              value={newName}
              onChange={e => setNewName(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && handleCreateProject()}
              autoFocus
            />
            <textarea
              className="input"
              placeholder="Work dirs (one per line)"
              value={newDirs}
              onChange={e => setNewDirs(e.target.value)}
              rows={2}
            />
            <button className="btn btn--primary" onClick={handleCreateProject}>Create</button>
          </div>
        )}

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
                  <span className="nav-tree__project-name">{p.name}</span>
                  <div className="nav-tree__actions">
                    {isActive && (
                      <button
                        className="icon-btn nav-tree__action-btn"
                        onClick={e => { e.stopPropagation(); handleNewSession(p) }}
                        title="New session"
                      >+</button>
                    )}
                    <button
                      className="icon-btn icon-btn--danger nav-tree__action-btn"
                      onClick={e => { e.stopPropagation(); deleteProject(p.id) }}
                      title="Delete project"
                    >×</button>
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
                            {editingId === s.id ? (
                              <input
                                className="input session-list__edit-input"
                                value={editingName}
                                onChange={e => setEditingName(e.target.value)}
                                onKeyDown={e => {
                                  e.stopPropagation()
                                  if (e.key === 'Enter') handleSaveEdit()
                                  if (e.key === 'Escape') { setEditingId(null); setEditingName('') }
                                }}
                                onBlur={handleSaveEdit}
                                onClick={e => e.stopPropagation()}
                                autoFocus
                              />
                            ) : (
                              <>
                                <span
                                  className="nav-tree__session-name"
                                  onDoubleClick={e => handleStartEdit(s, e)}
                                >
                                  {s.name || 'Untitled'}
                                </span>
                                <span className="nav-tree__session-meta">{s.message_count} msgs</span>
                              </>
                            )}
                          </div>
                          <button
                            className="icon-btn icon-btn--danger nav-tree__action-btn"
                            onClick={e => { e.stopPropagation(); deleteSession(s.id) }}
                            title="Delete session"
                          >×</button>
                        </li>
                      ))
                    )}
                  </ul>
                )}
              </li>
            )
          })}
        </ul>
      </div>
    </aside>
  )
}
