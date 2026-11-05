import { useSyncExternalStore } from 'react'

const QUERY = '(max-width: 700px)' // same breakpoint as the mobile rules in index.css

function subscribe(cb: () => void) {
  const mq = window.matchMedia(QUERY)
  mq.addEventListener('change', cb)
  return () => mq.removeEventListener('change', cb)
}

// True at phone width: the left bar is an overlay and the header actions move to the mobile bar.
export function useIsMobile() {
  return useSyncExternalStore(subscribe, () => window.matchMedia(QUERY).matches, () => false)
}
