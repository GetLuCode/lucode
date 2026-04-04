import { useState, useRef, useEffect } from 'react'
import { useStore } from '../../store'
import { useSessionWS } from '../../hooks/useSessionWS'
import { MessageBubble } from './MessageBubble'
import { ThinkingIndicator } from './ThinkingIndicator'
import { ToolApprovalModal } from './ToolApprovalModal'
import type { Message } from '../../types'

const EMPTY_MESSAGES: Message[] = []

const MODELS = [
  { label: 'Haiku 4.5', value: 'haiku' },
  { label: 'Sonnet 4.6', value: 'sonnet' },
  { label: 'Opus 4.6', value: 'opus' },
]

export function ChatView() {
  const activeSessionId = useStore(s => s.activeSessionId)
  const messages = useStore(s =>
    activeSessionId ? (s.messages[activeSessionId] ?? EMPTY_MESSAGES) : EMPTY_MESSAGES
  )
  const streaming = useStore(s => activeSessionId ? s.streaming[activeSessionId] : undefined)
  const sessionStatus = useStore(s =>
    activeSessionId ? (s.sessionStatus[activeSessionId] ?? 'idle') : 'idle'
  )
  const sessionUsage = useStore(s => activeSessionId ? s.sessionUsage[activeSessionId] : undefined)
  const sessions = useStore(s => s.sessions)
  const sendMessage = useStore(s => s.sendMessage)
  const pendingHook = useStore(s => s.pendingHook)

  const [input, setInput] = useState('')
  const [model, setModel] = useState('haiku')
  const bottomRef = useRef<HTMLDivElement>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const { sendHookRespond } = useSessionWS(activeSessionId)

  const session = sessions.find(s => s.id === activeSessionId)
  const isRunning = sessionStatus === 'running' || sessionStatus === 'waiting_hook'
  const hasStreamingText = streaming && streaming.text.length > 0

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages.length, streaming?.text])

  const handleSend = () => {
    if (!input.trim() || !activeSessionId || isRunning) return
    sendMessage(activeSessionId, input.trim(), model)
    setInput('')
    textareaRef.current?.focus()
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSend()
    }
  }

  if (!activeSessionId) {
    return (
      <div className="chat chat--empty">
        <div className="chat__empty-state">
          <div className="chat__empty-icon">◈</div>
          <p>Select or create a session to begin</p>
        </div>
      </div>
    )
  }

  return (
    <div className="chat">
      <div className="chat__header">
        <span className="chat__session-name">{session?.name || 'Session'}</span>
        <div className="chat__header-right">
          {sessionUsage && (
            <span className="chat__usage-total">
              ↑{fmt(sessionUsage.input_tokens)} ↓{fmt(sessionUsage.output_tokens)} tokens
            </span>
          )}
          <span className={`chat__status chat__status--${sessionStatus}`}>
            {sessionStatus === 'running' && 'thinking…'}
            {sessionStatus === 'waiting_hook' && 'awaiting approval'}
            {sessionStatus === 'idle' && 'ready'}
            {sessionStatus === 'error' && 'error'}
          </span>
        </div>
      </div>

      <div className="chat__messages">
        {messages.map((msg, i) => (
          <MessageBubble key={msg.id} message={msg} index={i} />
        ))}

        {/* Live streaming text */}
        {hasStreamingText && (
          <div className="msg msg--assistant">
            <div className="msg__bubble">
              <p className="msg__text msg__text--streaming">{streaming!.text}</p>
            </div>
          </div>
        )}

        {isRunning && !hasStreamingText && <ThinkingIndicator />}
        <div ref={bottomRef} />
      </div>

      <ToolApprovalModal
        hook={pendingHook && pendingHook.session_id === activeSessionId ? pendingHook : null}
        onApprove={() => sendHookRespond('approve')}
        onDeny={() => sendHookRespond('deny')}
      />

      <div className="chat__input-area">
        <div className="chat__input-wrap">
          <textarea
            ref={textareaRef}
            className="chat__input"
            placeholder={isRunning ? 'Claude is thinking…' : 'Message Claude…'}
            value={input}
            onChange={e => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            disabled={isRunning}
            rows={1}
          />
          <div className="chat__input-controls">
            <select
              className="model-select"
              value={model}
              onChange={e => setModel(e.target.value)}
              disabled={isRunning}
            >
              {MODELS.map(m => (
                <option key={m.value} value={m.value}>{m.label}</option>
              ))}
            </select>
            <button
              className="btn btn--send"
              onClick={handleSend}
              disabled={!input.trim() || isRunning}
            >
              ↑
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

function fmt(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n)
}
