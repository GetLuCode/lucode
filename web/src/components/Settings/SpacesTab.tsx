import { useEffect } from 'react'
import { useStore } from '../../store'
import type { InstanceInfo } from '../../types'
import './SpacesTab.css'

function osLabel(os: string) {
  if (os === 'windows') return 'Windows'
  if (os === 'darwin') return 'macOS'
  return 'Linux'
}

function InstanceCard({ info, badge }: { info: InstanceInfo; badge?: string }) {
  const elapsed = Math.round((Date.now() - new Date(info.started_at).getTime()) / 60000)
  const uptime = elapsed < 60 ? `${elapsed}m` : `${Math.round(elapsed / 60)}h`
  return (
    <div className="instance-card">
      <div className="instance-card__header">
        <span className="instance-card__type">{info.type}</span>
        {badge && <span className="instance-card__badge">{badge}</span>}
      </div>
      <div className="instance-card__hostname">{info.hostname}</div>
      <div className="instance-card__meta">{osLabel(info.os)} · up {uptime}</div>
      <div className="instance-card__id">{info.id.slice(0, 8)}</div>
    </div>
  )
}

export function SpacesTab() {
  const instances = useStore(s => s.instances)
  const projects = useStore(s => s.projects)
  const loadInstances = useStore(s => s.loadInstances)
  const setProjectAIAPI = useStore(s => s.setProjectAIAPI)

  useEffect(() => { loadInstances() }, [loadInstances])

  const aiapiPeers = instances.peers.filter(p => p.type === 'aiapi')

  return (
    <div className="spaces-tab">
      <section className="spaces-section">
        <h3 className="spaces-section__title">Discovered Instances</h3>
        <div className="spaces-instances">
          {instances.self && (
            <InstanceCard info={instances.self} badge="this" />
          )}
          {instances.peers.map(p => (
            <InstanceCard key={p.id} info={p} />
          ))}
          {!instances.self && instances.peers.length === 0 && (
            <p className="spaces-empty">No instances discovered yet. Make sure NATS is connected.</p>
          )}
        </div>
      </section>

      {aiapiPeers.length > 0 && (
        <section className="spaces-section">
          <h3 className="spaces-section__title">AI Backend per Project</h3>
          <p className="spaces-hint">Choose which aiapi instance executes AI turns for each project.</p>
          <div className="spaces-projects">
            {projects.map(p => (
              <div key={p.id} className="spaces-project-row">
                <span className="spaces-project-name">{p.name}</span>
                <select
                  className="spaces-aiapi-select"
                  value={p.aiapi_instance_id ?? ''}
                  onChange={e => setProjectAIAPI(p.id, e.target.value)}
                >
                  <option value="">Local (default)</option>
                  {aiapiPeers.map(a => (
                    <option key={a.id} value={a.id}>
                      {a.hostname} ({osLabel(a.os)})
                    </option>
                  ))}
                </select>
              </div>
            ))}
          </div>
        </section>
      )}
    </div>
  )
}
