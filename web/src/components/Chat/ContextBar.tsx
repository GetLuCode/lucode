import { useState, useEffect, useRef } from 'react'
import { useStore } from '../../store'
import { getFirstTurnUsage, getMinimaxTokenPlan } from '../../api'
import type { FirstTurnUsage } from '../../api'
import { IconX } from '@tabler/icons-react'
import { estimateCost, estimateUsageCost, formatCost } from '../../lib/tokenPricing'
import type { UsageTokens } from '../../lib/tokenPricing'
import type { ContextUsage } from '../../types'

// Maps Claude's internal color names to CSS colors
const COLOR_MAP: Record<string, string> = {
  promptBorder: '#6366f1',      // indigo — system prompt
  inactive: '#9ca3af',          // gray — system tools / compact buffer
  claude: '#d97706',            // amber — memory files
  warning: '#f59e0b',           // yellow — skills
  purple_FOR_SUBAGENTS_ONLY: '#8b5cf6', // purple — messages / subagents
  primary: '#3b82f6',           // blue fallback
  success: '#22c55e',           // green fallback
  tools: '#06b6d4',             // cyan — tool definitions
  cacheRead: '#22c55e',         // green — cached prompt tokens read (cheap)
  cacheWrite: '#f59e0b',        // amber — tokens written to cache
  input: '#3b82f6',             // blue — uncached input
  output: '#8b5cf6',            // purple — model output
}

function fmtTokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k'
  return String(n)
}

function fmtTimeUntil(dateStr: string): string {
  const resetAt = new Date(dateStr).getTime()
  const now = Date.now()
  const diff = resetAt - now
  if (diff <= 0) return 'ahora'
  const h = Math.floor(diff / 3_600_000)
  const m = Math.floor((diff % 3_600_000) / 60_000)
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

interface TokenPlan {
  remaining_tokens?: number
  total_tokens?: number
  reset_at?: string
  [key: string]: unknown
}

const TOKEN_PLAN_POLL_MS = 2 * 60 * 1000 // every 2 minutes

function TokenPlanBadge() {
  const [plan, setPlan] = useState<TokenPlan | null>(null)
  const [now, setNow] = useState(() => Date.now())
  const timerRef = useRef<ReturnType<typeof setInterval> | null>(null)

  const fetchPlan = async () => {
    try {
      const data = await getMinimaxTokenPlan()
      setPlan(data as TokenPlan)
    } catch {
      // Not configured or unreachable — stay hidden
    }
  }

  useEffect(() => {
    fetchPlan()
    const pollId = setInterval(fetchPlan, TOKEN_PLAN_POLL_MS)
    // Update the displayed countdown every minute
    timerRef.current = setInterval(() => setNow(Date.now()), 60_000)
    return () => {
      clearInterval(pollId)
      if (timerRef.current) clearInterval(timerRef.current)
    }
  }, [])

  if (!plan) return null

  const remaining = typeof plan.remaining_tokens === 'number' ? plan.remaining_tokens : null
  const total = typeof plan.total_tokens === 'number' ? plan.total_tokens : null
  const resetAt = typeof plan.reset_at === 'string' ? plan.reset_at : null

  if (remaining === null && resetAt === null) return null

  const pct = remaining !== null && total !== null && total > 0
    ? Math.round((remaining / total) * 100)
    : null
  const level = pct !== null ? (pct <= 5 ? 'danger' : pct <= 20 ? 'warning' : 'normal') : 'normal'

  void now // keep linter happy — used for countdown reactivity

  return (
    <div className={`token-plan-badge token-plan-badge--${level}`} title="MiniMax token plan">
      {remaining !== null && (
        <span className="token-plan-badge__remaining">
          ⚡ {fmtTokens(remaining)}{total !== null ? ` / ${fmtTokens(total)}` : ''}
        </span>
      )}
      {resetAt && (
        <span className="token-plan-badge__reset">
          ↺ {fmtTimeUntil(resetAt)}
        </span>
      )}
    </div>
  )
}

export type ModalSessionUsage = UsageTokens

// Categories that are not loaded context: free room, the autocompact reserve and deferred tools.
const isLoadedCategory = (name: string) =>
  name !== 'Free space' && name !== 'Autocompact buffer' && !name.endsWith('(deferred)')

// The context window broken down by category, plus what the session has billed. It has no
// overlay: it closes with Escape, its X, or a mousedown outside it (except on an element
// marked data-context-trigger, which toggles it itself).
export function ContextDetailModal({ contextUsage, sessionUsage, placement = 'viewport', onClose }: {
  contextUsage: ContextUsage // its session_id says which session's first turn to fetch
  sessionUsage?: ModalSessionUsage
  placement?: 'viewport' | 'panel'
  onClose: () => void
}) {
  const modalRef = useRef<HTMLDivElement>(null)
  const [firstTurn, setFirstTurn] = useState<FirstTurnUsage | null>(null)
  const sid = contextUsage.session_id
  useEffect(() => {
    setFirstTurn(null)
    if (!sid) return
    let cancelled = false
    getFirstTurnUsage(sid).then(u => { if (!cancelled) setFirstTurn(u) }).catch(() => {})
    return () => { cancelled = true }
  }, [sid])
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    const onDown = (e: MouseEvent) => {
      const t = e.target as HTMLElement
      if (modalRef.current?.contains(t) || t.closest?.('[data-context-trigger]')) return
      onClose()
    }
    document.addEventListener('keydown', onKey)
    document.addEventListener('mousedown', onDown)
    return () => {
      document.removeEventListener('keydown', onKey)
      document.removeEventListener('mousedown', onDown)
    }
  }, [onClose])

  const { categories, totalTokens, maxTokens } = contextUsage
  const usedCategories = (categories ?? []).filter(c => c.name !== 'Free space' && c.tokens > 0)
  const freeSpace = (categories ?? []).find(c => c.name === 'Free space')
  const pctUsed = maxTokens > 0 ? (totalTokens / maxTokens) * 100 : 0

  // Initial cost: what every turn carries before any message (system prompt, tools, skills,
  // MCP, memory files...) against the conversation itself.
  const loaded = (categories ?? []).filter(c => isLoadedCategory(c.name))
  const messages = loaded.find(c => c.name === 'Messages')?.tokens ?? 0
  const initial = loaded.reduce((n, c) => n + c.tokens, 0) - messages

  return (
    <div ref={modalRef} className={`voice-modal context-detail${placement === 'panel' ? ' context-detail--panel' : ''}`} role="dialog" aria-label="Context details">
      <div className="voice-modal__header">
        <h3 className="voice-modal__title">Context window</h3>
        <button className="voice-modal__close" onClick={onClose} title="Close" aria-label="Close">
          <IconX size={18} />
        </button>
      </div>
      <div className="voice-modal__body">
        <div className="context-detail__summary">
          <span>{contextUsage.model}</span>
          <span>{fmtTokens(totalTokens)} / {fmtTokens(maxTokens)} ({pctUsed.toFixed(1)}%)</span>
        </div>
        {loaded.length > 0 && (
          <>
            <div className="context-detail__section">Initial cost vs conversation</div>
            <div className="context-detail__row">
              <span className="context-detail__dot" style={{ backgroundColor: COLOR_MAP.promptBorder }} />
              <span className="context-detail__name" title="System prompt, tools, skills, MCP, memory files: what each turn carries before any message">Initial context</span>
              <span className="context-detail__tokens">{initial.toLocaleString()}</span>
              <span className="context-detail__pct">{maxTokens > 0 ? ((initial / maxTokens) * 100).toFixed(2) : '0'}%</span>
            </div>
            <div className="context-detail__row">
              <span className="context-detail__dot" style={{ backgroundColor: COLOR_MAP.purple_FOR_SUBAGENTS_ONLY }} />
              <span className="context-detail__name">Messages</span>
              <span className="context-detail__tokens">{messages.toLocaleString()}</span>
              <span className="context-detail__pct">{maxTokens > 0 ? ((messages / maxTokens) * 100).toFixed(2) : '0'}%</span>
            </div>
          </>
        )}
        {firstTurn && (() => {
          const model = firstTurn.model || contextUsage.model
          const written = firstTurn.cache_creation_input_tokens
          const read = firstTurn.cache_read_input_tokens
          const fresh = firstTurn.input_tokens
          const written1h = Math.min(firstTurn.cache_creation_1h_tokens ?? 0, written)
          const cWritten = estimateCost(model, 0, 0, 0, written - written1h, written1h)
          const cRead = estimateCost(model, 0, 0, read, 0)
          const cFresh = estimateCost(model, fresh, 0)
          const real = cWritten + cRead + cFresh
          const noCache = estimateCost(model, fresh + written + read, 0) // every input token at full price
          const row = (color: string, name: string, title: string, tokens: number, cost: number) => (
            <div className="context-detail__row" title={title}>
              <span className="context-detail__dot" style={{ backgroundColor: color }} />
              <span className="context-detail__name">{name}</span>
              <span className="context-detail__tokens">{tokens.toLocaleString()}</span>
              <span className="context-detail__pct">{formatCost(cost)}</span>
            </div>
          )
          return (
            <>
              <div
                className="context-detail__section"
                title={firstTurn.exact
                  ? 'The session’s very first call to the model: exactly what its initial context billed.'
                  : 'An older session: only its turn totals were kept, and those are the LAST call of the turn (after its tool results), so this is not the initial context. Sessions run now report every call.'}
              >{firstTurn.exact ? 'Initial context: real cost (first call)' : 'First turn, last call (older session: approximate)'}</div>
              {row(COLOR_MAP.cacheWrite, 'Written to cache', 'Not found in the cache: billed at 125% (5 min cache) or 200% (1 h cache) of the input price', written, cWritten)}
              {row(COLOR_MAP.cacheRead, 'Read from cache', 'Found in the cache (this or another session with the same prefix): billed at 10%', read, cRead)}
              {row(COLOR_MAP.input, 'Uncached input', 'Billed at the full input price', fresh, cFresh)}
              <div className="context-detail__row context-detail__row--muted">
                <span className="context-detail__name">Real cost / without cache</span>
                <span className="context-detail__tokens">{formatCost(real)} / {formatCost(noCache)}</span>
              </div>
            </>
          )
        })()}
        <div className="context-detail__section">Current context</div>
        {usedCategories.map(cat => (
          <div key={cat.name} className="context-detail__row">
            <span className="context-detail__dot" style={{ backgroundColor: COLOR_MAP[cat.color] ?? COLOR_MAP.primary }} />
            <span className="context-detail__name">{cat.name}</span>
            <span className="context-detail__tokens">{cat.tokens.toLocaleString()}</span>
            <span className="context-detail__pct">{maxTokens > 0 ? ((cat.tokens / maxTokens) * 100).toFixed(2) : '0'}%</span>
          </div>
        ))}
        {freeSpace && (
          <div className="context-detail__row context-detail__row--muted">
            <span className="context-detail__dot context-detail__dot--free" />
            <span className="context-detail__name">Free space</span>
            <span className="context-detail__tokens">{freeSpace.tokens.toLocaleString()}</span>
            <span className="context-detail__pct">{maxTokens > 0 ? ((freeSpace.tokens / maxTokens) * 100).toFixed(1) : '0'}%</span>
          </div>
        )}
        {sessionUsage && (
          <>
            <div className="context-detail__section">Session total (billed turns)</div>
            <div className="context-detail__row"><span className="context-detail__dot" style={{ backgroundColor: COLOR_MAP.input }} /><span className="context-detail__name">Input</span><span className="context-detail__tokens">{sessionUsage.input_tokens.toLocaleString()}</span></div>
            <div className="context-detail__row"><span className="context-detail__dot" style={{ backgroundColor: COLOR_MAP.output }} /><span className="context-detail__name">Output</span><span className="context-detail__tokens">{sessionUsage.output_tokens.toLocaleString()}</span></div>
            <div className="context-detail__row"><span className="context-detail__dot" style={{ backgroundColor: COLOR_MAP.cacheRead }} /><span className="context-detail__name">Cache read</span><span className="context-detail__tokens">{(sessionUsage.cache_read_input_tokens ?? 0).toLocaleString()}</span></div>
            <div className="context-detail__row"><span className="context-detail__dot" style={{ backgroundColor: COLOR_MAP.cacheWrite }} /><span className="context-detail__name">Cache write</span><span className="context-detail__tokens">{(sessionUsage.cache_creation_input_tokens ?? 0).toLocaleString()}</span></div>
            <div className="context-detail__row context-detail__row--muted">
              <span className="context-detail__name">Estimated cost</span>
              <span className="context-detail__tokens">
                {formatCost(estimateUsageCost(contextUsage.model, sessionUsage))}
              </span>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

interface ContextBarProps {
  sessionId: string
  // Where the details window opens, always centered and without an overlay:
  // 'viewport' (default) in the middle of the screen, 'panel' in the middle of the
  // nearest positioned ancestor (the orchestrator panel).
  placement?: 'viewport' | 'panel'
}

export function ContextBar({ sessionId, placement = 'viewport' }: ContextBarProps) {
  const contextUsage = useStore(s => s.contextUsage[sessionId])
  const sessionUsage = useStore(s => s.sessionUsage[sessionId])
  const [detailOpen, setDetailOpen] = useState(false)
  const [tooltip, setTooltip] = useState<{ name: string; tokens: number; pct: number; x: number } | null>(null)

  if (!contextUsage) return <TokenPlanBadge />

  const { categories, totalTokens, maxTokens, percentage } = contextUsage
  const usedCategories = (categories ?? []).filter(c => c.name !== 'Free space' && c.tokens > 0)
  const freeSpace = (categories ?? []).find(c => c.name === 'Free space')

  // Color for the used portion bar
  const pctUsed = maxTokens > 0 ? (totalTokens / maxTokens) * 100 : 0
  const dangerLevel = pctUsed >= 90 ? 'danger' : pctUsed >= 70 ? 'warning' : 'normal'

  return (
    <div className="context-bar" data-context-trigger>
      <div
        className="context-bar__track context-bar__track--clickable"
        onMouseLeave={() => setTooltip(null)}
        onClick={() => setDetailOpen(v => !v)}
        title="Click for context details"
      >
        {usedCategories.map((cat) => {
          const catPct = maxTokens > 0 ? (cat.tokens / maxTokens) * 100 : 0
          const color = COLOR_MAP[cat.color] ?? COLOR_MAP.primary
          return (
            <div
              key={cat.name}
              className="context-bar__segment"
              style={{ width: `${catPct}%`, backgroundColor: color }}
              onMouseEnter={(e) => {
                const rect = (e.currentTarget.parentElement as HTMLElement).getBoundingClientRect()
                const segRect = e.currentTarget.getBoundingClientRect()
                setTooltip({
                  name: cat.name,
                  tokens: cat.tokens,
                  pct: catPct,
                  x: segRect.left - rect.left + segRect.width / 2,
                })
              }}
            />
          )
        })}
        {/* Free space segment */}
        {freeSpace && (
          <div
            className="context-bar__segment context-bar__segment--free"
            style={{ width: `${maxTokens > 0 ? (freeSpace.tokens / maxTokens) * 100 : 0}%` }}
            onMouseEnter={(e) => {
              const rect = (e.currentTarget.parentElement as HTMLElement).getBoundingClientRect()
              const segRect = e.currentTarget.getBoundingClientRect()
              const freePct = maxTokens > 0 ? (freeSpace.tokens / maxTokens) * 100 : 0
              setTooltip({
                name: 'Free space',
                tokens: freeSpace.tokens,
                pct: freePct,
                x: segRect.left - rect.left + segRect.width / 2,
              })
            }}
          />
        )}

        {/* Tooltip */}
        {tooltip && (
          <div
            className="context-bar__tooltip"
            style={{ left: `${tooltip.x}px` }}
          >
            <span className="context-bar__tooltip-name">{tooltip.name}</span>
            <span className="context-bar__tooltip-tokens">{fmtTokens(tooltip.tokens)} tokens</span>
            <span className="context-bar__tooltip-pct">({tooltip.pct.toFixed(1)}%)</span>
          </div>
        )}
      </div>

      <div className={`context-bar__label context-bar__label--${dangerLevel}`}>
        {percentage}% &nbsp;
        <span className="context-bar__label-detail">
          {fmtTokens(totalTokens)} / {fmtTokens(maxTokens)}
        </span>
      </div>

      <TokenPlanBadge />

      {detailOpen && (
        <ContextDetailModal
          contextUsage={contextUsage}
          sessionUsage={sessionUsage}
          placement={placement}
          onClose={() => setDetailOpen(false)}
        />
      )}
    </div>
  )
}
