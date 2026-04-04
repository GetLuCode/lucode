import { useState } from 'react'
import { useStore } from '../../store'
import type { Project } from '../../types'
import './ProjectList.css'

export function ProjectList() {
  const projects = useStore(s => s.projects)
  const activeProjectId = useStore(s => s.activeProjectId)
  const setActiveProject = useStore(s => s.setActiveProject)
  const createProject = useStore(s => s.createProject)
  const deleteProject = useStore(s => s.deleteProject)
  const [showNew, setShowNew] = useState(false)
  const [name, setName] = useState('')
  const [workDirs, setWorkDirs] = useState('')

  const handleCreate = async () => {
    if (!name.trim()) return
    const dirs = workDirs.split('\n').map(d => d.trim()).filter(Boolean)
    const p = await createProject({ name: name.trim(), work_dirs: dirs })
    await setActiveProject(p.id)
    setShowNew(false)
    setName('')
    setWorkDirs('')
  }

  return (
    <div className="project-list">
      <div className="project-list__header">
        <span className="project-list__title">Projects</span>
        <button className="icon-btn" onClick={() => setShowNew(v => !v)} title="New project">+</button>
      </div>

      {showNew && (
        <div className="project-list__new">
          <input
            className="input"
            placeholder="Project name"
            value={name}
            onChange={e => setName(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && handleCreate()}
            autoFocus
          />
          <textarea
            className="input"
            placeholder="Work dirs (one per line)"
            value={workDirs}
            onChange={e => setWorkDirs(e.target.value)}
            rows={2}
          />
          <button className="btn btn--primary" onClick={handleCreate}>Create</button>
        </div>
      )}

      <ul className="project-list__items">
        {projects.map((p: Project) => (
          <li
            key={p.id}
            className={`project-list__item${activeProjectId === p.id ? ' project-list__item--active' : ''}`}
            onClick={() => setActiveProject(p.id)}
          >
            <span className="project-list__dot" />
            <span className="project-list__name">{p.name}</span>
            <button
              className="icon-btn icon-btn--danger"
              onClick={e => { e.stopPropagation(); deleteProject(p.id) }}
              title="Delete"
            >×</button>
          </li>
        ))}
      </ul>
    </div>
  )
}
