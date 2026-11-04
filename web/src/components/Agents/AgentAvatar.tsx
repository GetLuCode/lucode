import { useState } from 'react'
import './AgentAvatar.css'

const COLORS = ['#e5484d', '#f76b15', '#f5a524', '#30a46c', '#00a2c7', '#3e63dd', '#8e4ec6', '#d6409f']
function hashOf(s: string) { let h = 0; for (const ch of s) h = (h * 31 + ch.charCodeAt(0)) >>> 0; return h }

// Robot avatar of an agent: DiceBear "bottts-neutral" (free API) seeded with the agent name, so each
// agent name always gets the same one. If it cannot load (offline), the agent's initial on a color
// picked from the same hash.
export function AgentAvatar({ agent, size = 24, className = '' }: {
  agent: { id: string; name: string }; size?: number; className?: string
}) {
  const [failed, setFailed] = useState(false)
  const box = { width: size, height: size } as React.CSSProperties
  if (failed) {
    return (
      <span className={`agent-avatar agent-avatar--initial ${className}`}
        style={{ ...box, background: COLORS[hashOf(agent.id) % COLORS.length], fontSize: size * 0.46 }}>
        {(agent.name.trim()[0] ?? '?').toUpperCase()}
      </span>
    )
  }
  return (
    <img
      className={`agent-avatar ${className}`}
      style={box}
      src={`https://api.dicebear.com/10.x/bottts-neutral/svg?seed=${encodeURIComponent(agent.name.trim())}&radius=50`}
      alt=""
      loading="lazy"
      onError={() => setFailed(true)}
    />
  )
}
