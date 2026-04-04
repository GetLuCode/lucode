import { motion } from 'framer-motion'
import type { Message } from '../../types'

interface Props {
  message: Message
  index: number
}

export function MessageBubble({ message, index }: Props) {
  return (
    <motion.div
      className={`msg msg--${message.role}`}
      initial={{ opacity: 0, y: 8 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.2, delay: Math.min(index * 0.02, 0.3), ease: 'easeOut' }}
    >
      <div className="msg__bubble">
        <BubbleContent message={message} />
        {message.usage && <TokenBadge usage={message.usage} />}
      </div>
    </motion.div>
  )
}

function BubbleContent({ message }: { message: Message }) {
  switch (message.role) {
    case 'user':
      return <p className="msg__text">{message.content}</p>

    case 'assistant':
      return <p className="msg__text">{message.content}</p>

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

    default:
      return <pre className="msg__code">{message.content}</pre>
  }
}

function TokenBadge({ usage }: { usage: NonNullable<Message['usage']> }) {
  const parts: string[] = []
  if (usage.input_tokens) parts.push(`↑${fmt(usage.input_tokens)}`)
  if (usage.output_tokens) parts.push(`↓${fmt(usage.output_tokens)}`)
  if (usage.cache_read_input_tokens) parts.push(`cache↩${fmt(usage.cache_read_input_tokens)}`)

  return (
    <div className="msg__tokens">
      {parts.join('  ·  ')}
    </div>
  )
}

function fmt(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n)
}
