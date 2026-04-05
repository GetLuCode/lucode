import { motion, AnimatePresence } from 'framer-motion'
import { IconTrash } from '@tabler/icons-react'
import type { Message } from '../../types'

interface Props {
  isOpen: boolean
  messages: Message[]
  onClose: () => void
  onUnstar: (messageId: string, sessionId: string) => void
  onSelectMessage?: (messageId: string) => void
}

function getRoleIcon(role: string): string {
  switch (role) {
    case 'user': return '👤'
    case 'assistant': return '🤖'
    case 'thinking': return '◈'
    default: return '💬'
  }
}

function truncateText(text: string, maxLength: number = 120): string {
  return text.length > maxLength ? text.slice(0, maxLength) + '…' : text
}

function formatTime(dateStr: string): string {
  const d = new Date(dateStr)
  if (isNaN(d.getTime())) return ''
  const now = new Date()
  const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  if (d.toDateString() === now.toDateString()) return time
  const date = d.toLocaleDateString([], { month: 'short', day: 'numeric' })
  if (d.getFullYear() === now.getFullYear()) return `${date} · ${time}`
  return `${date} ${d.getFullYear()} · ${time}`
}

export function StarredPanel({ isOpen, messages, onClose, onUnstar, onSelectMessage }: Props) {
  if (!isOpen || messages.length === 0) {
    return (
      <AnimatePresence>
        {isOpen && (
          <motion.div
            className="starred-panel"
            initial={{ opacity: 0, x: 20 }}
            animate={{ opacity: 1, x: 0 }}
            exit={{ opacity: 0, x: 20 }}
            transition={{ duration: 0.2 }}
          >
            <div className="starred-panel__header">
              <span className="starred-panel__title">Starred</span>
              <button
                className="starred-panel__close"
                onClick={onClose}
                aria-label="Close starred messages"
              >
                ✕
              </button>
            </div>
            <div className="starred-panel__empty">No starred messages</div>
          </motion.div>
        )}
      </AnimatePresence>
    )
  }

  return (
    <AnimatePresence>
      {isOpen && (
        <motion.div
          className="starred-panel"
          initial={{ opacity: 0, x: 20 }}
          animate={{ opacity: 1, x: 0 }}
          exit={{ opacity: 0, x: 20 }}
          transition={{ duration: 0.2 }}
        >
          <div className="starred-panel__header">
            <span className="starred-panel__title">
              Starred
              <span className="starred-panel__count">{messages.length}</span>
            </span>
            <button
              className="starred-panel__close"
              onClick={onClose}
              aria-label="Close starred messages"
            >
              ✕
            </button>
          </div>

          <div className="starred-panel__list">
            {messages.map(msg => (
              <div
                key={msg.id}
                className="starred-panel__item"
                onClick={() => onSelectMessage?.(msg.id)}
              >
                <div className="starred-panel__item-header">
                  <span className="starred-panel__item-role">
                    {getRoleIcon(msg.role)} {msg.role}
                  </span>
                  <span className="starred-panel__item-time">
                    {formatTime(msg.created_at)}
                  </span>
                </div>
                <p className="starred-panel__item-content">
                  {truncateText(msg.content)}
                </p>
                <button
                  className="starred-panel__item-unstar"
                  onClick={(e) => {
                    e.stopPropagation()
                    onUnstar(msg.id, msg.session_id)
                  }}
                  aria-label="Remove star"
                  title="Remove star"
                >
                  <IconTrash size={14} />
                </button>
              </div>
            ))}
          </div>
        </motion.div>
      )}
    </AnimatePresence>
  )
}
