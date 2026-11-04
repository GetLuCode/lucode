import { useEffect, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { motion } from 'framer-motion'
import { marked, type Token, type TokensList } from 'marked'
import { IconVolume, IconVolumeOff, IconCopy, IconCheck, IconStar, IconStarFilled, IconBraces, IconZoomIn, IconZoomOut, IconZoomReset, IconArrowsMaximize, IconX } from '@tabler/icons-react'
import Zoom from 'react-medium-image-zoom'
import 'react-medium-image-zoom/dist/styles.css'
import { ToolInputRenderer } from '../Hook/HookToolInput'
import { ToolExecutionMessage } from './ToolExecutionMessage'
import { highlightCode } from '../../lib/highlight'
import { renderMermaid } from '../../lib/mermaid'
import type { Message, ToolInputField } from '../../types'

marked.use({
  gfm: true,
  breaks: false,
  renderer: {
    // fenced code gets syntax highlighting (the language comes after the opening fence)
    code({ text, lang }) {
      const { html, language } = highlightCode(text, lang)
      return `<pre><code class="hljs${language ? ` language-${language}` : ''}">${html}</code></pre>`
    },
  },
})

interface Props {
  message: Message
  sendHookRespond?: (toolUseId: string, decision: 'approve' | 'deny' | 'allow_always' | 'allow_with_rule' | 'deny_always', reason?: string, rule?: { tool: string; input_regex?: Record<string, string> }) => void
  isBeingRead?: boolean
  onSpeak?: (messageId: string, content: string) => void
  onStopSpeaking?: () => void
  isStarred?: boolean
  onStar?: (messageId: string) => void
  onViewRawData?: (messageId: string) => void
}

export function MessageBubble({ message, sendHookRespond, isBeingRead, onSpeak, onStopSpeaking, isStarred, onStar, onViewRawData }: Props) {
  const [copied, setCopied] = useState(false)

  const handleSpeak = () => {
    onSpeak?.(message.id, message.content)
  }

  const handleCopy = async () => {
    await navigator.clipboard.writeText(message.content)
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  const handleStar = () => {
    onStar?.(message.id)
  }

  return (
    <div className='msg-container'>
      <motion.div
        className={`msg msg--${message.role}`}
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        transition={{ duration: 0.15, ease: 'easeOut' }}
      >
        <div className={`msg__bubble${isBeingRead ? ' msg__bubble--reading' : ''}`}>
          <BubbleContent message={message} sendHookRespond={sendHookRespond} />
        </div> 
      </motion.div>
      <div className={`msg__info msg--${message.role}`}>
        {message.usage && <TokenBadge usage={message.usage} />}
        <time className={`msg__time msg__time--${message.role}`}>
          {fmtTime(message.created_at)}
        </time>
        <div className={`msg__actions-container msg--${message.role}`}>
            {(message.role === 'user' || message.role === 'assistant') && (
              <div className="msg__actions">
                <>
                {
                  message.role === 'assistant' && <div className="tooltip-wrapper">
                    {isBeingRead ? (
                      <button
                        className="msg__action-btn msg__action-btn--active"
                        onClick={onStopSpeaking}
                        aria-label="Stop reading"
                      >
                        <IconVolumeOff size={13} />
                      </button>
                    ) : (
                      <button className="msg__action-btn" onClick={handleSpeak} aria-label="Read aloud">
                        <IconVolume size={13} />
                      </button>
                    )}
                    <div className="tooltip tooltip--up">
                      {isBeingRead ? 'Stop reading' : 'Read aloud'}
                    </div>
                  </div>}
                  {message.role === 'assistant' &&<div className="tooltip-wrapper">
                    <button className="msg__action-btn" onClick={handleCopy} aria-label="Copy">
                      {copied ? <IconCheck size={13} /> : <IconCopy size={13} />}
                    </button>
                    <div className="tooltip tooltip--up">{copied ? 'Copied!' : 'Copy'}</div>
                  </div>}
                  <div className="tooltip-wrapper">
                    <button
                      className={`msg__action-btn msg__action-btn--star${isStarred ? ' msg__action-btn--starred' : ''}`}
                      onClick={handleStar}
                      aria-label={isStarred ? 'Remove star' : 'Star message'}
                    >
                      {isStarred ? <IconStarFilled size={13} /> : <IconStar size={13} />}
                    </button>
                    <div className="tooltip tooltip--up">{isStarred ? 'Starred' : 'Star'}</div>
                  </div>
                  {message.role === 'assistant' && onViewRawData && (
                    <div className="tooltip-wrapper">
                      <button
                        className="msg__action-btn"
                        onClick={() => onViewRawData(message.id)}
                        aria-label="View raw turn data"
                      >
                        <IconBraces size={13} />
                      </button>
                      <div className="tooltip tooltip--up">Raw turn data</div>
                    </div>
                  )}
                </>
              </div>
            )}
        </div>
      </div>
    </div>
  )
}

function BubbleContent({ message, sendHookRespond }: { message: Message; sendHookRespond?: (toolUseId: string, decision: 'approve' | 'deny' | 'allow_always' | 'allow_with_rule' | 'deny_always', reason?: string, rule?: { tool: string; input_regex?: Record<string, string> }) => void }) {
  switch (message.role) {
    case 'user':
      return <UserContent content={message.content} />

    case 'assistant':
      return <MarkdownContent content={message.content} />

    case 'thinking':
      return (
        <details className="msg__thinking">
          <summary className="msg__thinking-label">◈ Thinking</summary>
          <p className="msg__text msg__text--dim">{message.content}</p>
        </details>
      )

    case 'raw':
      return (
        <details className="msg__raw">
          <summary className="msg__raw-label">⚙ Raw event</summary>
          <pre className="msg__code">{message.content}</pre>
        </details>
      )

    case 'hook': {
      const hookData = JSON.parse(message.content) as {
        tool_name: string
        tool_input: unknown
        decision: 'pending' | 'approve' | 'deny' | 'timeout'
        tool_use_id?: string
        tool_input_schema?: ToolInputField[] | null
      }
      return (
        <HookMessage
          hookData={hookData}
          sendHookRespond={sendHookRespond}
        />
      )
    }

    case 'system':
      return <p className="msg__system-notice">{message.content}</p>

    case 'tool_execution':
      return <ToolExecutionMessage content={message.content} />

    default:
      return <pre className="msg__code">{message.content}</pre>
  }
}

function UserContent({ content }: { content: string }) {
  // Try to parse as JSON content blocks (with images)
  let blocks: Array<{ type: string; text?: string; source?: { type: string; media_type: string; data: string } }> = []
  let text = content

  try {
    const parsed = JSON.parse(content)
    if (Array.isArray(parsed)) {
      blocks = parsed
      text = ''
    }
  } catch {
    // Not JSON, treat as plain text
  }

  return (
    <div className="msg__user-content">
      {blocks.length > 0 ? (
        // Render content blocks
        <>
          {blocks.map((block, i) => {
            if (block.type === 'text' && block.text) {
              return (
                <p key={i} className="msg__text">
                  {block.text}
                </p>
              )
            } else if (block.type === 'image' && block.source?.type === 'base64') {
              const dataUrl = `data:${block.source.media_type};base64,${block.source.data}`
              return (
                <div key={i} className="msg__image-container">
                  <Zoom>
                    <img src={dataUrl} alt="user-content" className="msg__image" />
                  </Zoom>
                </div>
              )
            }
            return null
          })}
        </>
      ) : (
        // Render as plain text
        <p className="msg__text">{text}</p>
      )}
    </div>
  )
}

type Segment = { kind: 'html'; html: string } | { kind: 'mermaid'; source: string }

// The markdown as html pieces with the mermaid blocks apart, so each diagram is a React
// component of its own (the html is React's: drawing into it by hand would be undone on re-render).
function splitSegments(content: string): Segment[] {
  const tokens = marked.lexer(content)
  const out: Segment[] = []
  let buf: Token[] = []
  const flush = () => {
    if (!buf.length) return
    const piece = Object.assign([...buf], { links: tokens.links }) as unknown as TokensList
    out.push({ kind: 'html', html: marked.parser(piece) })
    buf = []
  }
  for (const t of tokens) {
    if (t.type === 'code' && /^mermaid/i.test((t.lang ?? '').trim())) {
      flush()
      out.push({ kind: 'mermaid', source: t.text })
    } else buf.push(t)
  }
  flush()
  return out
}

function MarkdownContent({ content }: { content: string }) {
  const segments = useMemo(() => splitSegments(content), [content])
  return (
    <div className="msg__markdown">
      {segments.map((seg, i) => seg.kind === 'mermaid'
        ? <MermaidDiagram key={i} source={seg.source} />
        : <div key={i} className="msg__md-part" dangerouslySetInnerHTML={{ __html: seg.html }} />)}
    </div>
  )
}

// A mermaid block: the diagram once drawn; as code while it loads, if it is still streaming in
// or if it is not valid mermaid.
function MermaidDiagram({ source }: { source: string }) {
  const [svg, setSvg] = useState<string | null>(null)
  useEffect(() => {
    let cancelled = false
    setSvg(null)
    renderMermaid(source).then(r => { if (!cancelled) setSvg(r) })
    return () => { cancelled = true }
  }, [source])
  if (svg) return <DiagramView svg={svg} />
  const { html } = highlightCode(source, '')
  return <pre><code className="hljs" dangerouslySetInnerHTML={{ __html: html }} /></pre>
}

const ZOOM_MIN = 0.5
const ZOOM_MAX = 6

// A drawn diagram: zoomable in place, and openable in a view that covers the whole window.
function DiagramView({ svg }: { svg: string }) {
  const [full, setFull] = useState(false)
  return (
    <>
      <ZoomBox svg={svg} extra={
        <button type="button" onClick={() => setFull(true)} title="Full window" aria-label="Full window"><IconArrowsMaximize size={15} /></button>
      } />
      {full && createPortal(<DiagramOverlay svg={svg} onClose={() => setFull(false)} />, document.body)}
    </>
  )
}

function DiagramOverlay({ svg, onClose }: { svg: string; onClose: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') { e.stopPropagation(); onClose() } }
    window.addEventListener('keydown', onKey, true)
    return () => window.removeEventListener('keydown', onKey, true)
  }, [onClose])
  return (
    <div className="mermaid-overlay" onClick={e => { if (e.target === e.currentTarget) onClose() }}>
      <ZoomBox svg={svg} full extra={
        <button type="button" onClick={onClose} title="Close (Esc)" aria-label="Close"><IconX size={15} /></button>
      } />
    </div>
  )
}

// The zoom of a diagram: the buttons, Ctrl + wheel, and dragging to move around when it is
// bigger than its box. Zoom 1 is the diagram fitted to the width of its box.
function ZoomBox({ svg, extra, full }: { svg: string; extra: React.ReactNode; full?: boolean }) {
  const [zoom, setZoom] = useState(1)
  const box = useRef<HTMLDivElement>(null)
  const drag = useRef<{ x: number; y: number; left: number; top: number } | null>(null)
  const clamp = (z: number) => Math.min(ZOOM_MAX, Math.max(ZOOM_MIN, Math.round(z * 100) / 100))
  const step = (f: number) => setZoom(z => clamp(z * f))

  // the wheel must not scroll the page while zooming, which React's passive listeners cannot stop
  useEffect(() => {
    const el = box.current
    if (!el) return
    const onWheel = (e: WheelEvent) => {
      if (!e.ctrlKey) return
      e.preventDefault()
      setZoom(z => clamp(z * (e.deltaY < 0 ? 1.15 : 1 / 1.15)))
    }
    el.addEventListener('wheel', onWheel, { passive: false })
    return () => el.removeEventListener('wheel', onWheel)
  }, [])

  const onPointerDown = (e: React.PointerEvent<HTMLDivElement>) => {
    const el = box.current
    if (!el || e.button !== 0 || (el.scrollWidth <= el.clientWidth && el.scrollHeight <= el.clientHeight)) return
    drag.current = { x: e.clientX, y: e.clientY, left: el.scrollLeft, top: el.scrollTop }
    el.setPointerCapture(e.pointerId)
  }
  const onPointerMove = (e: React.PointerEvent<HTMLDivElement>) => {
    const d = drag.current, el = box.current
    if (!d || !el) return
    el.scrollLeft = d.left - (e.clientX - d.x)
    el.scrollTop = d.top - (e.clientY - d.y)
  }
  const endDrag = () => { drag.current = null }

  return (
    <div className={`mermaid-diagram${full ? ' mermaid-diagram--full' : ''}`}>
      <div className="mermaid-diagram__tools">
        <button type="button" onClick={() => step(1 / 1.25)} disabled={zoom <= ZOOM_MIN} title="Zoom out" aria-label="Zoom out"><IconZoomOut size={15} /></button>
        <button type="button" onClick={() => setZoom(1)} disabled={zoom === 1} title="Fit to width" aria-label="Reset zoom"><IconZoomReset size={15} /></button>
        <button type="button" onClick={() => step(1.25)} disabled={zoom >= ZOOM_MAX} title="Zoom in (Ctrl + wheel)" aria-label="Zoom in"><IconZoomIn size={15} /></button>
        {extra}
      </div>
      <div
        ref={box}
        className="mermaid-diagram__scroll"
        onPointerDown={onPointerDown}
        onPointerMove={onPointerMove}
        onPointerUp={endDrag}
        onPointerCancel={endDrag}
      >
        <div className="mermaid-diagram__svg" style={{ width: `${zoom * 100}%`, minWidth: full ? undefined : `${zoom * 560}px` }} dangerouslySetInnerHTML={{ __html: svg }} />
      </div>
    </div>
  )
}

function TokenBadge({ usage }: { usage: NonNullable<Message['usage']> }) {
  const parts: string[] = []
  if (usage.input_tokens)                parts.push(`↑${fmt(usage.input_tokens)}`)
  if (usage.output_tokens)               parts.push(`↓${fmt(usage.output_tokens)}`)
  if (usage.cache_creation_input_tokens) parts.push(`cache✎${fmt(usage.cache_creation_input_tokens)}`)
  if (usage.cache_read_input_tokens)     parts.push(`cache↩${fmt(usage.cache_read_input_tokens)}`)

  return <div className="msg__tokens">{parts.join('  ·  ')}</div>
}

function fmtTime(dateStr: string): string {
  const d = new Date(dateStr)
  if (isNaN(d.getTime())) return ''
  const now = new Date()
  const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  if (d.toDateString() === now.toDateString()) return time
  const date = d.toLocaleDateString([], { month: 'short', day: 'numeric' })
  if (d.getFullYear() === now.getFullYear()) return `${date} · ${time}`
  return `${date} ${d.getFullYear()} · ${time}`
}

function fmt(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n)
}

interface HookData {
  tool_name: string
  tool_input: unknown
  decision: 'pending' | 'approve' | 'deny' | 'timeout'
  tool_use_id?: string
  tool_input_schema?: ToolInputField[] | null
}

const DECISION_ICON: Record<string, string> = {
  pending: '⏳',
  approve: '✓',
  deny: '✕',
  timeout: '⏱',
}

function HookMessage({
  hookData,
  sendHookRespond,
}: {
  hookData: HookData
  sendHookRespond?: (
    toolUseId: string,
    decision: 'approve' | 'deny' | 'allow_always' | 'allow_with_rule' | 'deny_always',
    reason?: string,
    rule?: { tool: string; input_regex?: Record<string, string> }
  ) => void
}) {
  const isPending = hookData.decision === 'pending'
  const [editOpen, setEditOpen] = useState(false)
  const [regexByField, setRegexByField] = useState<Record<string, string>>({})

  const schema: ToolInputField[] = useMemo(() => {
    if (hookData.tool_input_schema && hookData.tool_input_schema.length > 0) {
      return hookData.tool_input_schema
    }
    // Fall back to inferring from the input object keys.
    if (hookData.tool_input && typeof hookData.tool_input === 'object') {
      return Object.keys(hookData.tool_input as Record<string, unknown>).map((k) => ({
        name: k,
        type: 'string',
      }))
    }
    return []
  }, [hookData.tool_input_schema, hookData.tool_input])

  const handleApprove = () => {
    if (hookData.tool_use_id && sendHookRespond) {
      sendHookRespond(hookData.tool_use_id, 'approve')
    }
  }
  const handleAllowAlways = () => {
    if (hookData.tool_use_id && sendHookRespond) {
      sendHookRespond(hookData.tool_use_id, 'allow_always')
    }
  }
  const handleDeny = () => {
    if (hookData.tool_use_id && sendHookRespond) {
      sendHookRespond(hookData.tool_use_id, 'deny')
    }
  }
  const handleSubmitRule = () => {
    if (!hookData.tool_use_id || !sendHookRespond) return
    // Build input_regex from the modal state — only include fields
    // the user actually filled in.
    const input_regex: Record<string, string> = {}
    for (const [k, v] of Object.entries(regexByField)) {
      if (v.trim() !== '') input_regex[k] = v.trim()
    }
    sendHookRespond(hookData.tool_use_id, 'allow_with_rule', '', {
      tool: hookData.tool_name,
      input_regex,
    })
    setEditOpen(false)
  }

  return (
    <div className="msg__hook">
      <div className="msg__hook-header">
        <div className="msg__hook-title">
          ⚙ Tool Use — {hookData.tool_name}
          <span className={`msg__hook-decision-icon msg__hook-decision-icon--${hookData.decision}`}>
            {DECISION_ICON[hookData.decision]}
          </span>
        </div>
      </div>
      <div className="msg__hook-input">
        <ToolInputRenderer toolName={hookData.tool_name} input={hookData.tool_input} />
      </div>
      {isPending && (
        <div className="msg__hook-actions">
          <button className="btn btn--deny" onClick={handleDeny}>
            Deny
          </button>
          <button className="btn btn--approve" onClick={handleApprove}>
            Approve
          </button>
          <button className="btn btn--approve" onClick={handleAllowAlways}>
            Allow always
          </button>
          <button className="btn" onClick={() => setEditOpen((v) => !v)}>
            Edit permission…
          </button>
        </div>
      )}
      {editOpen && isPending && (
        <div className="msg__hook-edit">
          <div className="msg__hook-edit-title">Allow {hookData.tool_name} only when input matches these regexes:</div>
          {schema.length === 0 ? (
            <div className="msg__hook-edit-empty">No known input fields for this tool.</div>
          ) : (
            schema.map((field) => (
              <label key={field.name} className="msg__hook-edit-row">
                <span className="msg__hook-edit-label">{field.name}{field.required ? ' *' : ''}</span>
                <input
                  type="text"
                  placeholder="regex, e.g. ^\\./src/.*"
                  value={regexByField[field.name] ?? ''}
                  onChange={(e) =>
                    setRegexByField((prev) => ({ ...prev, [field.name]: e.target.value }))
                  }
                />
              </label>
            ))
          )}
          <div className="msg__hook-edit-actions">
            <button className="btn" onClick={() => setEditOpen(false)}>
              Cancel
            </button>
            <button className="btn btn--approve" onClick={handleSubmitRule}>
              Allow with rule
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
