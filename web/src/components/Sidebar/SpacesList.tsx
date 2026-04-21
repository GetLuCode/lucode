import { useEffect } from 'react'
import { useStore } from '../../store'
import type { InstanceInfo } from '../../types'
import './SpacesList.css'

function osIcon(os: string) {
  if (os === 'windows') return '⊞'
  if (os === 'darwin') return ''
  return '🐧'
}

function SpaceItem({ info, active, onClick }: {
  info: InstanceInfo & { label: string }
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      className={`space-item ${active ? 'space-item--active' : ''}`}
      onClick={onClick}
      title={`${info.hostname} · ${info.os}`}
    >
      <span className="space-item__icon">{osIcon(info.os)}</span>
      <span className="space-item__label">{info.label}</span>
      {active && <span className="space-item__dot" />}
    </button>
  )
}

export function SpacesList() {
  const instances = useStore(s => s.instances)
  const activeRemoteId = useStore(s => s.activeRemoteId)
  const loadInstances = useStore(s => s.loadInstances)
  const setActiveRemote = useStore(s => s.setActiveRemote)

  useEffect(() => {
    loadInstances()
    const id = setInterval(loadInstances, 20_000)
    return () => clearInterval(id)
  }, [loadInstances])

  const lucodeInstances = instances.peers.filter(p => p.type === 'lucode')

  if (lucodeInstances.length === 0 && !instances.self) return null

  const self = instances.self
  const selfLabel = self ? `${self.hostname} (this)` : 'Local'

  return (
    <div className="spaces-list">
      <div className="spaces-list__header">Spaces</div>

      <SpaceItem
        info={{ ...(self ?? { id: '', type: 'lucode', hostname: 'local', os: 'linux', started_at: '' }), label: selfLabel }}
        active={activeRemoteId === null}
        onClick={() => setActiveRemote(null)}
      />

      {lucodeInstances.map(peer => (
        <SpaceItem
          key={peer.id}
          info={{ ...peer, label: peer.hostname }}
          active={activeRemoteId === peer.id}
          onClick={() => setActiveRemote(peer.id)}
        />
      ))}
    </div>
  )
}
