import { useState } from 'react'
import type { CSSProperties } from 'react'
import { useNavigate } from 'react-router-dom'
import { useStore } from '../../store'
import type { Project } from '../../types'
import { IconAdjustments, IconChartBar, IconLogout, IconCpu } from '@tabler/icons-react'
import { WorkspacePicker } from './WorkspacePicker'
import { getProjectColor, getProjectInitials } from './projectStyle'
import { SettingsModal } from '../Settings/SettingsModal'
import { TaskManagerPanel } from '../TaskManager/TaskManagerPanel'
import { useAuthStore } from '../../store/auth'
import * as api from '../../api'

// The icon rail: projects, usage, task manager, settings and sign out.
// Everything about the active project, and the search, live in the project header.
export function NavPanel({ mobileOpen = false, onMobileClose }: { mobileOpen?: boolean; onMobileClose?: () => void }) {
  const navigate = useNavigate()
  const projects = useStore(s => s.projects)
  const activeProjectId = useStore(s => s.activeProjectId)
  const setActiveProject = useStore(s => s.setActiveProject)
  const createProject = useStore(s => s.createProject)

  const [showWorkspacePicker, setShowWorkspacePicker] = useState(false)
  const [showSettings, setShowSettings] = useState(false)
  const [showTaskManager, setShowTaskManager] = useState(false)

  const handleSelectProject = async (id: string) => {
    await setActiveProject(id)
    navigate(`/projects/${id}/agent`)
  }

  const handleWorkspacePickerConfirm = async (name: string, path: string, env: { runnerId: string; aiProfileId: string }) => {
    // work_dirs are folders of THIS machine; a project on a peer runner keeps its folder
    // only in env_dirs, which is where that runner looks.
    const remote = env.runnerId !== ''
    const p: Project = await createProject({
      name,
      work_dirs: remote ? [] : [path],
      ...(env.aiProfileId ? { ai_profile_id: env.aiProfileId } : {}),
      ...(remote ? { runner_id: env.runnerId, env_dirs: { [env.runnerId]: [path] } } : {}),
    })
    await setActiveProject(p.id)
    navigate(`/projects/${p.id}/agent`)
  }

  return (
    <aside className={`nav-panel nav-panel--collapsed${mobileOpen ? ' nav-panel--mobile-open' : ''}`}>
      <div className="nav-panel__backdrop" onClick={onMobileClose} />
      <div className="nav-panel__rail">
        <button className="nav-panel__rail-logo" onClick={() => navigate('/')} title="Home" aria-label="Home" style={{ background: 'none', border: 'none', cursor: 'pointer' }}>
          <img src={new URL('../../../public/logo-icon.svg', import.meta.url).href} alt="LuCode" width="20" height="20" />
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
        <button className="nav-panel__rail-btn" onClick={() => navigate('/usage')} title="Usage metrics">
          <IconChartBar size={16} />
        </button>
        <button className="nav-panel__rail-btn" title="Task Manager" onClick={() => setShowTaskManager(true)}>
          <IconCpu size={16} />
        </button>
        <button className="nav-panel__rail-btn" title="Settings" onClick={() => setShowSettings(true)}>
          <IconAdjustments size={16} />
        </button>
        <LogoutButton />
        <SettingsModal isOpen={showSettings} onClose={() => setShowSettings(false)} />
        <TaskManagerPanel isOpen={showTaskManager} onClose={() => setShowTaskManager(false)} />
      </div>

      <WorkspacePicker
        isOpen={showWorkspacePicker}
        onClose={() => setShowWorkspacePicker(false)}
        onConfirm={handleWorkspacePickerConfirm}
      />
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
    <button className="nav-panel__rail-btn" onClick={handleLogout} title="Sign out">
      <IconLogout size={16} />
    </button>
  )
}
