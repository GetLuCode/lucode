import { useEffect, useState } from 'react'
import type { CSSProperties } from 'react'
import { motion } from 'framer-motion'
import { useStore } from '../../store'
import { usePlugins } from '../../store/plugins'
import { normalizeModelId } from '../../lib/plugins'
import { ModelIcon } from '../Orchestrator/ModelIcon'
import '../Orchestrator/ModelIcon.css'

function clock(seconds: number) {
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`
}

// A turn in progress. With a sessionId it also shows how long the turn has been going and what
// it says it is doing (e.g. "Reopening the saved conversation… 12 of 40 turns loaded"), which
// matters when the wait is long. A plugin's model can ask for a loader of its own: its mark turning
// and skeleton lines where the answer will be, with the words its manifest gives.
export function ThinkingIndicator({ sessionId, model }: { sessionId?: string | null; model?: string }) {
  const progress = useStore(s => (sessionId ? s.turnProgress[sessionId] : '') ?? '')
  const plugin = usePlugins(s => (model ? s.ui.models.find(m => m.value === normalizeModelId(model)) : undefined))
  const [seconds, setSeconds] = useState(0)
  useEffect(() => {
    const started = Date.now()
    const id = setInterval(() => setSeconds(Math.floor((Date.now() - started) / 1000)), 1000)
    return () => clearInterval(id)
  }, [])

  if (model && plugin?.loader === 'skeleton') {
    return (
      <div className="model-loader" role="status" aria-live="polite" style={plugin.accent ? { '--model-accent': plugin.accent } as CSSProperties : undefined}>
        <div className="model-loader__head">
          <ModelIcon model={model} size={18} className="model-loader__mark" />
          <span className="model-loader__text">{progress || plugin.waitingText || `Waiting for ${plugin.label}…`}</span>
          <span className="model-loader__time" title="Time since this turn started">{clock(seconds)}</span>
        </div>
        <div className="model-loader__bar" style={{ width: '92%' }} />
        <div className="model-loader__bar" style={{ width: '78%' }} />
        <div className="model-loader__bar" style={{ width: '54%' }} />
      </div>
    )
  }

  return (
    <div className="thinking">
      {[0, 1, 2].map(i => (
        <motion.span
          key={i}
          className="thinking__dot"
          animate={{ opacity: [0.3, 1, 0.3] }}
          transition={{
            duration: 1.2,
            repeat: Infinity,
            delay: i * 0.2,
            ease: 'easeInOut',
          }}
        />
      ))}
      {sessionId && progress && <span className="thinking__text">{progress}</span>}
      {sessionId && (progress || seconds >= 3) && <span className="thinking__time" title="Time since this turn started">{clock(seconds)}</span>}
    </div>
  )
}
