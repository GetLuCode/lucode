const BASE = ''

export interface Idea {
  id: string
  scope: string
  text: string
  done: boolean
  created_at: string
}

export async function fetchIdeas(scope: string): Promise<Idea[]> {
  const res = await fetch(`${BASE}/api/ideas?scope=${encodeURIComponent(scope)}`)
  if (!res.ok) throw new Error(await res.text())
  return res.json()
}

export async function createIdea(scope: string, text: string): Promise<Idea> {
  const res = await fetch(`${BASE}/api/ideas`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ scope, text }),
  })
  if (!res.ok) throw new Error(await res.text())
  return res.json()
}

export async function toggleIdea(id: string, done: boolean): Promise<void> {
  const res = await fetch(`${BASE}/api/ideas/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ done }),
  })
  if (!res.ok) throw new Error(await res.text())
}

export async function deleteIdea(id: string): Promise<void> {
  const res = await fetch(`${BASE}/api/ideas/${id}`, { method: 'DELETE' })
  if (!res.ok) throw new Error(await res.text())
}
