import { useCallback, useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { IconDownload, IconEdit, IconTrash } from '@tabler/icons-react'
import { useStore } from '../../store'
import * as api from '../../api'
import { AgentAvatar } from '../Agents/AgentAvatar'
import '../Flows/Flows.css'
import { ContextDetailModal } from '../Chat/ContextBar'
import { estimateUsageCost, formatCost, formatTokens } from '../../lib/tokenPricing'
import type { ActiveFlowRun, Agent, ContextUsage, KanbanCard, Session } from '../../types'

// Tokens a session has used, added up over all its turns and models.
interface TokenTotals { input: number; output: number; cacheRead: number; cacheWrite: number; cacheWrite1h: number }

// Last context-window size the session reported (after its latest turn, so for a finished task
// it is the final one). The session keeps it as JSON.
function lastContext(s: Session, live?: ContextUsage): ContextUsage | null {
  if (live && live.totalTokens > 0) return live
  if (!s.context_usage) return null
  try {
    const c = JSON.parse(s.context_usage)
    return typeof c.totalTokens === 'number' && c.totalTokens > 0 ? { ...c, session_id: s.id } : null
  } catch { return null }
}

// The flows in progress and where each one is. The detail, node by node, is in the Flows tab.
function ActiveFlows({ projectId }: { projectId: string }) {
  const navigate = useNavigate()
  const flowVersion = useStore(s => s.flowVersion)
  const [runs, setRuns] = useState<ActiveFlowRun[]>([])
  useEffect(() => {
    let cancelled = false
    api.listActiveFlowRuns(projectId).then(r => { if (!cancelled) setRuns(r) }).catch(console.error)
    return () => { cancelled = true }
  }, [projectId, flowVersion])
  if (runs.length === 0) return null
  return (
    <div className="running-flows">
      {runs.map(r => (
        <button
          key={r.run.id}
          className="running-flows__item"
          onClick={() => navigate(`/projects/${projectId}/flows?flow=${r.run.flow_id}&run=${r.run.id}`)}
        >
          <strong>{r.flow_name}</strong>
          <span>{r.open_names.join(', ') || 'Starting'}</span>
          {r.card_title && <span>{r.card_title}</span>}
        </button>
      ))}
    </div>
  )
}

// Running: the project's current sessions (the chats of its tasks), live ones first.
export function RunningView() {
  const navigate = useNavigate()
  const sessions = useStore(s => s.sessions)
  const activeProjectId = useStore(s => s.activeProjectId)
  const activeSessionId = useStore(s => s.activeSessionId)
  const sessionStatus = useStore(s => s.sessionStatus)
  const contextUsage = useStore(s => s.contextUsage)
  const setActiveSession = useStore(s => s.setActiveSession)
  const deleteSession = useStore(s => s.deleteSession)
  const renameSession = useStore(s => s.renameSession)

  const [menuId, setMenuId] = useState<string | null>(null)
  const [editingId, setEditingId] = useState<string | null>(null)
  const [editingName, setEditingName] = useState('')

  const [cards, setCards] = useState<Record<string, KanbanCard>>({})
  const [agents, setAgents] = useState<Record<string, Agent>>({})
  const [lists, setLists] = useState<Record<string, string>>({})
  const [costs, setCosts] = useState<Record<string, number>>({})
  const [tokens, setTokens] = useState<Record<string, TokenTotals>>({})
  // the session whose context breakdown is open
  const [ctxId, setCtxId] = useState<string | null>(null)
  const closeCtx = useCallback(() => setCtxId(null), [])
  useEffect(() => {
    if (!activeProjectId) return
    let cancelled = false
    const load = () => Promise.all([
      api.listKanbanCards(activeProjectId),
      api.listAgents(activeProjectId),
      api.listKanbanLists(activeProjectId),
      api.getUsageSessions('all', activeProjectId),
    ]).then(([cs, ags, ls, usage]) => {
      if (cancelled) return
      setCards(Object.fromEntries(cs.map(c => [c.id, c])))
      setAgents(Object.fromEntries(ags.map(a => [a.id, a])))
      setLists(Object.fromEntries(ls.map(l => [l.id, l.name])))
      // One row per session and model: add them up into the session's estimated cost.
      const byS: Record<string, number> = {}
      const tokS: Record<string, TokenTotals> = {}
      for (const u of usage) {
        byS[u.session_id] = (byS[u.session_id] ?? 0) +
          estimateUsageCost(u.model, u)
        const t = tokS[u.session_id] ??= { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, cacheWrite1h: 0 }
        t.cacheWrite1h += u.cache_creation_1h_input_tokens ?? 0
        t.input += u.input_tokens
        t.output += u.output_tokens
        t.cacheRead += u.cache_read_input_tokens
        t.cacheWrite += u.cache_creation_input_tokens
      }
      setCosts(byS)
      setTokens(tokS)
    }).catch(() => {})
    load()
    const t = setInterval(load, 10000)
    return () => { cancelled = true; clearInterval(t) }
  }, [activeProjectId])

  // Lines and files the task's branch changes against its base. git runs on the runner, so it is
  // asked again only when a session's activity or status changes, not on every poll.
  const [diffs, setDiffs] = useState<Record<string, api.DiffStat>>({})
  const diffKey = sessions
    .filter(s => s.card_id && (s.branch || s.worktree))
    .map(s => `${s.id}:${s.last_active}:${sessionStatus[s.id] ?? s.status}`)
    .join('|')
  useEffect(() => {
    if (!diffKey) return
    let cancelled = false
    for (const part of diffKey.split('|')) {
      const id = part.slice(0, part.indexOf(':'))
      api.getSessionDiffStat(id).then(d => {
        if (!cancelled) setDiffs(prev => ({ ...prev, [id]: d }))
      }).catch(() => {})
    }
    return () => { cancelled = true }
  }, [diffKey])

  const fmtFull = (iso?: string) => {
    if (!iso) return '—'
    const d = new Date(iso)
    return isNaN(d.getTime()) || d.getFullYear() < 2000 ? '—' : d.toLocaleString([], { dateStyle: 'medium', timeStyle: 'medium' })
  }
  // 42s, 7m 05s, 3h 12m, 2d 4h
  const fmtDur = (ms: number) => {
    const sec = Math.max(0, Math.round(ms / 1000))
    if (sec < 60) return `${sec}s`
    const min = Math.floor(sec / 60)
    if (min < 60) return `${min}m ${String(sec % 60).padStart(2, '0')}s`
    const h = Math.floor(min / 60)
    if (h < 24) return `${h}h ${String(min % 60).padStart(2, '0')}m`
    return `${Math.floor(h / 24)}d ${h % 24}h`
  }

  const statusOf =(s: Session) => sessionStatus[s.id] ?? s.status
  const rank = (s: Session) => (statusOf(s) === 'idle' ? 1 : 0)
  // Only task sessions (tied to a kanban card), live ones first; the store keeps the rest by activity.
  const taskSessions = sessions.filter(s => s.card_id)
  // A session is finished once its task has a finished_at.
  const isFinished = (s: Session) => !!(s.card_id && cards[s.card_id]?.finished_at)
  const [tab, setTab] = useState<'current' | 'finished'>('current')
  const nFinished = taskSessions.filter(isFinished).length
  const list = taskSessions.filter(s => isFinished(s) === (tab === 'finished')).sort((a, b) => rank(a) - rank(b))

  const dot = (s: Session) => {
    const st = statusOf(s)
    if (st === 'running') return '●'
    if (st === 'waiting_hook') return '◆'
    if (st === 'error') return '✕'
    return '○'
  }

  async function saveEdit() {
    if (editingId && editingName.trim()) await renameSession(editingId, editingName.trim())
    setEditingId(null)
    setEditingName('')
  }

  async function exportSession(e: React.MouseEvent, s: Session) {
    e.stopPropagation()
    setMenuId(null)
    try {
      const blob = await api.exportSession(s.id)
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = `${(s.name || 'session').replace(/[^a-zA-Z0-9_-]/g, '_')}.json`
      a.click()
      URL.revokeObjectURL(url)
    } catch (err) {
      console.error('export failed', err)
    }
  }

  return (
    <div className="running-view">
      {activeProjectId && <ActiveFlows projectId={activeProjectId} />}
      <div className="agent-form__tabs">
        <button className={`agent-form__tab${tab === 'current' ? ' agent-form__tab--active' : ''}`} onClick={() => setTab('current')}>
          Current <span className="agent-form__tab-badge">{taskSessions.length - nFinished}</span>
        </button>
        <button className={`agent-form__tab${tab === 'finished' ? ' agent-form__tab--active' : ''}`} onClick={() => setTab('finished')}>
          Finished <span className="agent-form__tab-badge">{nFinished}</span>
        </button>
      </div>
      {list.length === 0 ? (
        <div className="running-view__empty">{tab === 'finished' ? 'No finished tasks yet.' : 'No sessions yet. Move a task to In Progress to start one.'}</div>
      ) : (
        <ul className="running-list">
          {list.map(s => (
            <li
              key={s.id}
              className={`running-item${activeSessionId === s.id ? ' running-item--active' : ''}`}
              onClick={() => {
                setActiveSession(s.id)
                navigate(`/projects/${activeProjectId}/sessions/${s.id}`)
              }}
            >
              <span className={`status-dot status-dot--${statusOf(s)}`}>{dot(s)}</span>
              <div className="running-item__info">
                {editingId === s.id ? (
                  <input
                    className="input session-list__edit-input"
                    value={editingName}
                    onChange={e => setEditingName(e.target.value)}
                    onKeyDown={e => {
                      e.stopPropagation()
                      if (e.key === 'Enter') saveEdit()
                      if (e.key === 'Escape') { setEditingId(null); setEditingName('') }
                    }}
                    onBlur={saveEdit}
                    onClick={e => e.stopPropagation()}
                    autoFocus
                  />
                ) : (
                  <>
                    {(() => {
                      const c = s.card_id ? cards[s.card_id] : undefined
                      const startIso = c?.started_at || s.created_at
                      const startMs = new Date(startIso).getTime()
                      // Finished tasks stop the clock at finished_at; a live session runs until now;
                      // an idle one stops at its last activity.
                      const endIso = c?.finished_at || (statusOf(s) === 'running' || statusOf(s) === 'waiting_hook' ? '' : s.last_active)
                      const endMs = endIso ? new Date(endIso).getTime() : Date.now()
                      const duration = isNaN(startMs) || isNaN(endMs) ? '—' : fmtDur(endMs - startMs)
                      const dateTip = [
                        `Started: ${fmtFull(startIso)}`,
                        `Last active: ${fmtFull(s.last_active)}`,
                        `Finished: ${fmtFull(c?.finished_at)}`,
                      ].join('\n')
                      const listName = c ? lists[c.list_id] : ''
                      const cost = costs[s.id]
                      const tok = tokens[s.id]
                      const tokTotal = tok ? tok.input + tok.output + tok.cacheRead + tok.cacheWrite : 0
                      const ctx = lastContext(s, contextUsage[s.id])
                      const errText = c?.last_error || (statusOf(s) === 'error' ? 'The session ended with an error.' : '')
                      const ag = agents[s.agent_id || c?.agent_id || '']
                      const model = s.model || ag?.model
                      const agentInfo = ag && [
                        ag.name,
                        model,
                        ag.executor,
                        ag.runner_id ? `runner ${ag.runner_id}` : '',
                        ag.persistent ? 'persistent' : '',
                        ag.worktree ? 'worktree' : '',
                        ag.auto_mode ? 'auto' : '',
                      ].filter(Boolean).join(' · ')
                      const wtName = s.worktree ? s.worktree.split(/[\\/]/).filter(Boolean).pop() : ''
                      return (
                        <>
                          <div className="running-item__top">
                            <span className="running-item__name" title={s.name || 'Untitled'}>{s.name || 'Untitled'}</span>
                            {ag && (
                              <span className="running-item__agent" title={agentInfo}>
                                <AgentAvatar agent={ag} size={20} />
                                <span className="running-item__agent-name">{ag.name}</span>
                                {model && <span className="running-item__agent-model">{model}</span>}
                              </span>
                            )}
                          </div>
                          <div className="running-item__chips">
                            {listName && <span className="running-chip running-chip--status" title="Task status (its kanban list)">{listName}</span>}
                            <span className={`running-chip running-chip--${statusOf(s)}`} title="Session status">{statusOf(s)}</span>
                            <span className="running-chip" title="Messages in the session">{s.message_count} msgs</span>
                            {tok && (
                              <span
                                className="running-chip"
                                title={`Tokens used over all turns, counting the whole context each turn re-reads\n` +
                                  `${tok.input.toLocaleString()} input · ${tok.output.toLocaleString()} output\n` +
                                  `${tok.cacheRead.toLocaleString()} cache read · ${tok.cacheWrite.toLocaleString()} cache write`}
                              >{formatTokens(tokTotal)} tok</span>
                            )}
                            {ctx && (
                              <button
                                type="button"
                                data-context-trigger
                                className="running-chip running-chip--button"
                                onClick={e => { e.stopPropagation(); setCtxId(ctxId === s.id ? null : s.id) }}
                                title={`Context window after the latest turn${c?.finished_at ? ' (final: the task is finished)' : ''}: ` +
                                  `${ctx.totalTokens.toLocaleString()}${ctx.maxTokens ? ` of ${ctx.maxTokens.toLocaleString()} tokens (${ctx.percentage}%)` : ' tokens'}\nClick for the full breakdown`}
                              >ctx {formatTokens(ctx.totalTokens)}{ctx.maxTokens ? ` · ${ctx.percentage}%` : ''}</button>
                            )}
                            {cost !== undefined && (
                              <span className="running-chip" title="Estimated cost from the tokens used (list prices, not billed)">~{formatCost(cost)}</span>
                            )}
                          </div>
                          {(() => {
                            const d = diffs[s.id]
                            if (!d || d.error) return null
                            const files = d.created + d.modified + d.deleted
                            if (files === 0 && d.added === 0 && d.removed === 0) return null
                            const tip = `Whole branch ${d.branch} against ${d.base}` +
                              `\n${d.created} created · ${d.modified} modified · ${d.deleted} deleted`
                            return (
                              <div className="running-item__diff" title={tip}>
                                <span className="running-diff running-diff--add">+{d.added}</span>
                                <span className="running-diff running-diff--del">−{d.removed}</span>
                                <span className="running-diff__files">
                                  {d.created > 0 && <span className="running-diff--add">{d.created} new</span>}
                                  {d.modified > 0 && <span>{d.modified} mod</span>}
                                  {d.deleted > 0 && <span className="running-diff--del">{d.deleted} del</span>}
                                </span>
                              </div>
                            )
                          })()}
                          {errText && <div className="running-item__error" title={errText}>{errText}</div>}
                          <div className="running-item__bottom">
                            <span className="running-item__meta running-item__mono">
                              {(s.branch || wtName) && (
                                <span className="running-item__worktree" title={s.worktree}>
                                  {s.branch ? `⎇ ${s.branch}` : wtName}
                                </span>
                              )}
                            </span>
                            <span className="running-item__date" title={dateTip}>⏱ {duration}</span>
                          </div>
                        </>
                      )
                    })()}
                  </>
                )}
              </div>
              <div className="nav-tree__menu-container">
                <button
                  className="icon-btn nav-tree__action-btn"
                  onClick={e => { e.stopPropagation(); setMenuId(menuId === s.id ? null : s.id) }}
                  title="Session menu"
                >⋯</button>
                {menuId === s.id && (
                  <div className="nav-tree__dropdown">
                    <button
                      className="nav-tree__menu-item"
                      onClick={e => { e.stopPropagation(); setEditingId(s.id); setEditingName(s.name); setMenuId(null) }}
                    >
                      <IconEdit size={14} />
                      <span>Rename</span>
                    </button>
                    <button className="nav-tree__menu-item" onClick={e => exportSession(e, s)}>
                      <IconDownload size={14} />
                      <span>Export</span>
                    </button>
                    <button
                      className="nav-tree__menu-item nav-tree__menu-item--danger"
                      onClick={e => { e.stopPropagation(); deleteSession(s.id); setMenuId(null) }}
                    >
                      <IconTrash size={14} />
                      <span>Delete</span>
                    </button>
                  </div>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
      {ctxId && (() => {
        const sess = sessions.find(x => x.id === ctxId)
        const cu = sess && lastContext(sess, contextUsage[ctxId])
        const t = tokens[ctxId]
        if (!cu) return null
        return (
          <ContextDetailModal
            contextUsage={cu}
            sessionUsage={t && {
              input_tokens: t.input, output_tokens: t.output,
              cache_read_input_tokens: t.cacheRead, cache_creation_input_tokens: t.cacheWrite,
              cache_creation_1h_input_tokens: t.cacheWrite1h,
            }}
            onClose={closeCtx}
          />
        )
      })()}
    </div>
  )
}
