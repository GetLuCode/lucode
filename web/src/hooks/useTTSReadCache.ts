import { useEffect, useRef, useState } from 'react'
import { ttsDb } from '../lib/ttsDb'

/**
 * Persists auto-read message IDs in IndexedDB so a message is never
 * auto-read again after a page reload. Manual "Read aloud" is unaffected.
 */
export function useTTSReadCache() {
  const readIds = useRef(new Set<string>())
  const [loaded, setLoaded] = useState(false)

  useEffect(() => {
    ttsDb.readMessages.toArray().then((items) => {
      items.forEach((i) => readIds.current.add(i.id))
      setLoaded(true)
    })
  }, [])

  const markRead = (id: string) => {
    readIds.current.add(id)
    ttsDb.readMessages.put({ id }) // fire-and-forget
  }

  const isRead = (id: string) => readIds.current.has(id)

  return { isRead, markRead, loaded }
}
