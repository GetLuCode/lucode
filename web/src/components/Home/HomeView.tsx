import { useEffect } from 'react'
import { useNavigate } from 'react-router-dom'
import { useStore } from '../../store'
import type { Project } from '../../types'
import { getProjectColor, getProjectInitials } from '../Sidebar/projectStyle'

// A zero date (the project has no activity yet) comes back as year 1.
function usedAt(p: Project): number | null {
  const t = p.last_used_at ? Date.parse(p.last_used_at) : NaN
  return Number.isFinite(t) && new Date(t).getFullYear() > 2000 ? t : null
}

function ago(t: number): string {
  const s = Math.max(0, Math.round((Date.now() - t) / 1000))
  if (s < 60) return 'just now'
  const m = Math.round(s / 60)
  if (m < 60) return `${m} min ago`
  const h = Math.round(m / 60)
  if (h < 24) return `${h} h ago`
  const d = Math.round(h / 24)
  if (d < 30) return `${d} d ago`
  return new Date(t).toLocaleDateString()
}

// Start page: the projects, most recently used first.
export function HomeView() {
  const navigate = useNavigate()
  const projects = useStore(s => s.projects)
  const setActiveProject = useStore(s => s.setActiveProject)
  const loadProjects = useStore(s => s.loadProjects)

  // Refresh on every visit so "last used" is current.
  useEffect(() => { loadProjects().catch(console.error) }, [loadProjects])

  const sorted = [...projects].sort((a, b) => {
    const ta = usedAt(a) ?? Date.parse(a.created_at) ?? 0
    const tb = usedAt(b) ?? Date.parse(b.created_at) ?? 0
    return tb - ta
  })

  async function open(p: Project) {
    await setActiveProject(p.id)
    navigate(`/projects/${p.id}/kanban`)
  }

  return (
    <div className="home-view">
      <h1 className="home-view__title">Projects</h1>
      {sorted.length === 0 ? (
        <p className="home-view__empty">No projects yet. Create one with the + in the left bar.</p>
      ) : (
        <ul className="home-list">
          {sorted.map(p => {
            const t = usedAt(p)
            return (
              <li key={p.id}>
                <button className="home-item" onClick={() => open(p)}>
                  <span className="home-item__badge" style={{ background: getProjectColor(p.name) }}>
                    {getProjectInitials(p.name)}
                  </span>
                  <span className="home-item__text">
                    <span className="home-item__name">{p.name}</span>
                    <span className="home-item__dir">{p.work_dirs?.[0] || (p.runner_id ? 'on another runner' : '')}</span>
                  </span>
                  <span className="home-item__used">{t ? ago(t) : 'not used yet'}</span>
                </button>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
