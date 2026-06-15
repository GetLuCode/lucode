import { useState, useEffect, useRef } from 'react'
import { useStore } from '../../store'
import { getMinimaxTokenPlan } from '../../api'

// Maps Claude's internal color names to CSS colors
const COLOR_MAP: Record<string, string> = {
  promptBorder: '#6366f1',      // indigo — system prompt
  inactive: '#9ca3af',          // gray — system tools / compact buffer
  claude: '#d97706',            // amber — memory files
  warning: '#f59e0b',           // yellow — skills
  purple_FOR_SUBAGENTS_ONLY: '#8b5cf6', // purple — messages / subagents
  primary: '#3b82f6',           // blue fallback
  success: '#22c55e',           // green fallback
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

interface ContextBarProps {
  sessionId: string
}

export function ContextBar({ sessionId }: ContextBarProps) {
  const contextUsage = useStore(s => s.contextUsage[sessionId])
  const [tooltip, setTooltip] = useState<{ name: string; tokens: number; pct: number; x: number } | null>(null)

  if (!contextUsage) return <TokenPlanBadge />

  const { categories, totalTokens, maxTokens, percentage } = contextUsage
  console.log('[ContextBar] contextUsage:', contextUsage, 'categories:', categories)
  const usedCategories = (categories ?? []).filter(c => c.name !== 'Free space' && c.tokens > 0)
  const freeSpace = (categories ?? []).find(c => c.name === 'Free space')

  // Color for the used portion bar
  const pctUsed = maxTokens > 0 ? (totalTokens / maxTokens) * 100 : 0
  const dangerLevel = pctUsed >= 90 ? 'danger' : pctUsed >= 70 ? 'warning' : 'normal'

  return (
    <div className="context-bar">
      <div
        className="context-bar__track"
        onMouseLeave={() => setTooltip(null)}
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
    </div>
  )
}
