import { useState } from 'react'
import '../Agents/AgentAvatar.css'

const COLORS = ['#e5484d', '#f76b15', '#f5a524', '#30a46c', '#00a2c7', '#3e63dd', '#8e4ec6', '#d6409f']
function hashOf(s: string) { let h = 0; for (const ch of s) h = (h * 31 + ch.charCodeAt(0)) >>> 0; return h }

// Avatar of an AI account: DiceBear "loops" (free API) seeded with the opaque profile id, so the
// same account always gets the same one and its email is never sent out. If it cannot load
// (offline), the initial of the name on a color picked from the same hash.
export function AccountAvatar({ id, name, size = 22 }: { id: string; name: string; size?: number }) {
  const [failed, setFailed] = useState(false)
  const box = { width: size, height: size } as React.CSSProperties
  if (failed) {
    return (
      <span className="agent-avatar agent-avatar--initial"
        style={{ ...box, background: COLORS[hashOf(id) % COLORS.length], fontSize: size * 0.46 }}>
        {(name.trim()[0] ?? '?').toUpperCase()}
      </span>
    )
  }
  return (
    <img
      className="agent-avatar"
      style={box}
      src={`https://api.dicebear.com/10.x/loops/svg?seed=${encodeURIComponent(id)}&radius=50`}
      alt=""
      loading="lazy"
      onError={() => setFailed(true)}
    />
  )
}
