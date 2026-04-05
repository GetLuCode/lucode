import type { Project, Session, MessagesPage } from './types'

const BASE = 'http://localhost:8090'

async function req<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(`${BASE}${path}`, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : {},
    body: body ? JSON.stringify(body) : undefined,
  })
  if (!res.ok) {
    const text = await res.text()
    throw new Error(`${method} ${path} → ${res.status}: ${text}`)
  }
  if (res.status === 204 || res.headers.get('content-length') === '0') {
    return undefined as T
  }
  return res.json()
}

// Projects
export const listProjects = () => req<Project[]>('GET', '/api/projects')
export const createProject = (data: Partial<Project>) => req<Project>('POST', '/api/projects', data)
export const deleteProject = (id: string) => req<void>('DELETE', `/api/projects/${id}`)

// Sessions
export const listSessions = (pid: string) => req<Session[]>('GET', `/api/projects/${pid}/sessions`)
export const createSession = (pid: string, data: { message?: string; name?: string; model?: string }) =>
  req<Session>('POST', `/api/projects/${pid}/sessions`, data)
export const deleteSession = (id: string) => req<void>('DELETE', `/api/sessions/${id}`)
export const renameSession = (id: string, name: string) =>
  req<void>('PATCH', `/api/sessions/${id}`, { name })
export const sendTurn = (id: string, message: string, model?: string) =>
  req<void>('POST', `/api/sessions/${id}/turn`, { message, model })
export const forkSession = (id: string, name: string, message?: string) =>
  req<Session>('POST', `/api/sessions/${id}/fork`, { name, message })

// Messages
export const listMessages = (id: string, opts?: { limit?: number; beforeSeq?: number }) => {
  const params = new URLSearchParams()
  if (opts?.limit != null) params.set('limit', String(opts.limit))
  if (opts?.beforeSeq != null) params.set('before_seq', String(opts.beforeSeq))
  const qs = params.toString()
  return req<MessagesPage>('GET', `/api/sessions/${id}/messages${qs ? `?${qs}` : ''}`)
}
