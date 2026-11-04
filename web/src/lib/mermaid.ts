// Mermaid diagrams of the chats. Always the light theme on a white card: diagrams set their own
// light fills (rect rgb(...), notes), which a dark theme leaves unreadable.
// The library is big, so it is only loaded when an answer has one.
let loading: Promise<typeof import('mermaid').default> | null = null
const cache = new Map<string, string | null>()
let seq = 0

function load() {
  loading ??= import('mermaid').then(m => {
    m.default.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'default' })
    return m.default
  })
  return loading
}

// The SVG of a diagram, or null if the source is not valid mermaid (yet).
export async function renderMermaid(source: string): Promise<string | null> {
  const src = source.trim()
  if (!src) return null
  if (cache.has(src)) return cache.get(src)!
  try {
    const mermaid = await load()
    const { svg } = await mermaid.render(`mmd-${++seq}`, src)
    cache.set(src, svg)
    return svg
  } catch {
    document.getElementById(`dmmd-${seq}`)?.remove() // mermaid leaves its scratch node behind on errors
    cache.set(src, null)
    return null
  }
}
