// Remembers which answers were already read aloud, across page reloads.
// A reload can re-deliver the last answer as if it were live (message ids are
// regenerated), so auto-read keys on session + content instead of the id.
// Only auto-read consults this; the manual "read aloud" button always plays.

const KEY = 'lucode.spoken'
const KEEP = 40

function fingerprint(sessionId: string, content: string): string {
  let h = 5381
  for (let i = 0; i < content.length; i++) h = ((h << 5) + h + content.charCodeAt(i)) | 0
  return `${sessionId}:${content.length}:${(h >>> 0).toString(36)}`
}

function load(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(KEY) ?? '[]')
    return Array.isArray(v) ? v : []
  } catch {
    return []
  }
}

export function wasSpoken(sessionId: string, content: string): boolean {
  return load().includes(fingerprint(sessionId, content))
}

export function markSpoken(sessionId: string, content: string): void {
  try {
    const fp = fingerprint(sessionId, content)
    const list = load().filter(x => x !== fp)
    list.push(fp)
    localStorage.setItem(KEY, JSON.stringify(list.slice(-KEEP)))
  } catch {
    /* storage unavailable: worst case it reads again after a reload */
  }
}
