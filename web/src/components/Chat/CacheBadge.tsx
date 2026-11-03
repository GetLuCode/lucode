import { useEffect, useRef, useState } from 'react'
import './CacheBadge.css'

// Anthropic's prompt cache lives 5 minutes after the last request.
export const CACHE_TTL_MS = 5 * 60 * 1000

function fmtClock(ms: number) {
  const s = Math.max(0, Math.ceil(ms / 1000))
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

export type CacheCountdown = {
  label: string
  state: 'busy' | 'none' | 'expired' | 'low' | 'ok'
  fraction: number
}

// Counts the prompt cache down from the end of the last turn. While a turn runs the
// cache is being refreshed, so the clock is held at the full 5 minutes. `sessionKey`
// restarts it when the component shows another session.
export function useCacheCountdown(status: string, lastActive: string | undefined, sessionKey = ''): CacheCountdown {
  const [endsAt, setEndsAt] = useState<number | null>(null)
  const [now, setNow] = useState(() => Date.now())
  const prev = useRef(status)

  // A chat opened with an earlier conversation: its cache ends 5 min after it was last active.
  useEffect(() => {
    prev.current = status
    if (lastActive && status === 'idle') {
      const t = new Date(lastActive).getTime()
      setEndsAt(Number.isNaN(t) ? null : t + CACHE_TTL_MS)
    } else if (!lastActive) {
      setEndsAt(null)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [lastActive, sessionKey])

  useEffect(() => {
    if (prev.current !== 'idle' && status === 'idle') setEndsAt(Date.now() + CACHE_TTL_MS)
    prev.current = status
  }, [status])

  const running = status === 'running' || status === 'waiting_hook'
  const expired = endsAt !== null && endsAt - now <= 0
  useEffect(() => {
    if (running || endsAt === null || expired) return
    setNow(Date.now())
    const id = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(id)
  }, [running, endsAt, expired])

  if (running) return { label: fmtClock(CACHE_TTL_MS), state: 'busy', fraction: 1 }
  if (endsAt === null) return { label: '—', state: 'none', fraction: 0 }
  const left = endsAt - now
  if (left <= 0) return { label: 'cache expired', state: 'expired', fraction: 0 }
  return { label: fmtClock(left), state: left < 60_000 ? 'low' : 'ok', fraction: left / CACHE_TTL_MS }
}

// `compact` is the narrow vertical form (the collapsed orchestrator tab): it only shows a
// running clock, never the "cache expired" text, which does not fit there.
export function CacheBadge({ cache, compact }: { cache: CacheCountdown; compact?: boolean }) {
  if (compact && (cache.state === 'expired' || cache.state === 'none')) return null
  const title = 'Prompt cache: time left before the 5-minute cache expires. A new message within it is cheaper and faster.'
  return (
    <span className={`cache-badge cache-badge--${cache.state}${compact ? ' cache-badge--compact' : ''}`} title={title}>
      <span className="cache-badge__bar"><span style={{ transform: `scaleX(${cache.fraction.toFixed(3)})` }} /></span>
      <span className="cache-badge__time">{cache.label}</span>
    </span>
  )
}
