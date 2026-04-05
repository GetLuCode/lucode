import { useMemo } from 'react'
import { motion } from 'framer-motion'
import { marked } from 'marked'
import Zoom from 'react-medium-image-zoom'
import 'react-medium-image-zoom/dist/styles.css'
import { ToolInputRenderer } from '../Hook/HookToolInput'
import type { Message } from '../../types'

marked.use({ gfm: true, breaks: false })

interface Props {
  message: Message
}

export function MessageBubble({ message }: Props) {
  return (
    <div>
      <motion.div
        className={`msg msg--${message.role}`}
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        transition={{ duration: 0.15, ease: 'easeOut' }}
      >
        <div className="msg__bubble">
          <BubbleContent message={message} />
          {message.usage && <TokenBadge usage={message.usage} />}
          <time className={`msg__time msg__time--${message.role}`}>
            {fmtTime(message.created_at)}
          </time>
        </div>
      </motion.div>
    </div>
  )
}

function BubbleContent({ message }: { message: Message }) {
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
      }
      const decisionIcon = {
        pending: '⏳',
        approve: '✓',
        deny: '✕',
        timeout: '⏱',
      }[hookData.decision]
      const decisionLabel = {
        pending: 'Pending…',
        approve: 'Approved',
        deny: 'Denied',
        timeout: 'Timed out',
      }[hookData.decision]
      const decisionClass = `msg__hook-decision msg__hook-decision--${hookData.decision}`
      return (
        <div className="msg__hook">
          <div className="msg__hook-header">
            <div>
              <div className="msg__hook-title">⚙ Tool Use — {hookData.tool_name}</div>
              <div className={decisionClass}>
                {decisionIcon} {decisionLabel}
              </div>
            </div>
          </div>
          <div className="msg__hook-input">
            <ToolInputRenderer toolName={hookData.tool_name} input={hookData.tool_input} />
          </div>
        </div>
      )
    }

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

function MarkdownContent({ content }: { content: string }) {
  const html = useMemo(() => marked.parse(content) as string, [content])
  return <div className="msg__markdown" dangerouslySetInnerHTML={{ __html: html }} />
}

function TokenBadge({ usage }: { usage: NonNullable<Message['usage']> }) {
  const parts: string[] = []
  if (usage.input_tokens)            parts.push(`↑${fmt(usage.input_tokens)}`)
  if (usage.output_tokens)           parts.push(`↓${fmt(usage.output_tokens)}`)
  if (usage.cache_read_input_tokens) parts.push(`cache↩${fmt(usage.cache_read_input_tokens)}`)

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
