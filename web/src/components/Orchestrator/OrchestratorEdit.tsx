import { useState } from 'react'
import { IconCheck, IconFolder, IconPencil } from '@tabler/icons-react'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import * as api from '../../api'
import type { Project, Session } from '../../types'

// Rename the current conversation and assign it to a project, so it can be found later in the history.
export function OrchestratorEdit({ projectId, sessionId, onMoved, onSaved }: {
  projectId: string
  sessionId: string
  onMoved: (s: Session) => void
  onSaved?: () => void
}) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [project, setProject] = useState(projectId)
  const [projects, setProjects] = useState<Project[]>([])
  const [error, setError] = useState('')

  const load = () => {
    setError('')
    setProject(projectId)
    api.getOrchestrator(projectId).then(s => setName(s.name === 'Orchestrator' ? '' : s.name)).catch(e => setError(String(e?.message ?? e)))
    api.listProjects().then(p => setProjects(p ?? [])).catch(() => {})
  }
  const save = async () => {
    setError('')
    try {
      const data: { name: string; project_id?: string } = { name: name.trim() }
      if (project !== projectId) data.project_id = project
      let s = await api.updateOrchestratorChat(sessionId, data)
      if (project !== projectId) s = await api.openOrchestratorChat(sessionId) // it is the one shown in its new project
      setOpen(false)
      onSaved?.()
      if (project !== projectId) onMoved(s)
    } catch (e) {
      setError(String((e as Error)?.message ?? e))
    }
  }

  return (
    <Popover open={open} onOpenChange={v => { setOpen(v); if (v) load() }}>
      <PopoverTrigger asChild>
        <button className="orch__icon-btn" title="Rename this conversation / assign it to a project" aria-label="Edit conversation">
          <IconPencil size={16} />
        </button>
      </PopoverTrigger>
      <PopoverContent side="bottom" align="end" className="conv-history orch-history">
        <div className="conv-history__title">This conversation</div>
        {error && <div className="conv-history__error">{error}</div>}
        <div className="orch-history__edit">
          <input
            autoFocus
            value={name}
            placeholder="Conversation name"
            onChange={e => setName(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') save() }}
          />
          <div className="orch-history__edit-row">
            <IconFolder size={13} />
            <select value={project} onChange={e => setProject(e.target.value)}>
              {!projects.some(p => p.id === projectId) && <option value={projectId}>Current project</option>}
              {projects.map(p => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
            <button className="orch__icon-btn" onClick={save} title="Save" aria-label="Save"><IconCheck size={14} /></button>
          </div>
        </div>
      </PopoverContent>
    </Popover>
  )
}
