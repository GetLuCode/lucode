import { useEffect, useState } from 'react'
import { hookSecondsLeft } from './hookSummary'

/** Seconds left to answer an approval created at createdAt. */
export function useHookCountdown(createdAt: string | undefined): number {
  const [remaining, setRemaining] = useState(() => hookSecondsLeft(createdAt, Date.now()))

  useEffect(() => {
    const tick = () => {
      const left = hookSecondsLeft(createdAt, Date.now())
      setRemaining(left)
      return left
    }
    tick()
    const interval = setInterval(() => {
      if (tick() === 0) clearInterval(interval)
    }, 1000)
    return () => clearInterval(interval)
  }, [createdAt])

  return remaining
}
