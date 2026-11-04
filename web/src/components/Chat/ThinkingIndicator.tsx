import { useEffect, useState } from 'react'
import { motion } from 'framer-motion'
import { useStore } from '../../store'
import { ModelIcon, isGeminiModel } from '../Orchestrator/ModelIcon'
import '../Orchestrator/ModelIcon.css'

function clock(seconds: number) {
  return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, '0')}`
}

// A turn in progress. With a sessionId it also shows how long the turn has been going and what
// it says it is doing (e.g. "Reopening the saved conversation… 12 of 40 turns loaded"), which
// matters when the wait is long. For the local Google AI Mode model (Gemini) it is a loader of
// its own: a spinning star and skeleton lines where the answer will be.
export function ThinkingIndicator({ sessionId, model }: { sessionId?: string | null; model?: string }) {
  const progress = useStore(s => (sessionId ? s.turnProgress[sessionId] : '') ?? '')
  const [seconds, setSeconds] = useState(0)
  useEffect(() => {
    const started = Date.now()
    const id = setInterval(() => setSeconds(Math.floor((Date.now() - started) / 1000)), 1000)
    return () => clearInterval(id)
  }, [])

  if (model && isGeminiModel(model)) {
    return (
      <div className="gemini-loader" role="status" aria-live="polite">
        <div className="gemini-loader__head">
          <ModelIcon model={model} size={18} className="gemini-loader__star" />
          <span className="gemini-loader__text">{progress || 'Waiting for Google AI Mode…'}</span>
          <span className="gemini-loader__time" title="Time since this turn started">{clock(seconds)}</span>
        </div>
        <div className="gemini-loader__bar" style={{ width: '92%' }} />
        <div className="gemini-loader__bar" style={{ width: '78%' }} />
        <div className="gemini-loader__bar" style={{ width: '54%' }} />
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
