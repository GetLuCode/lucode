import { useState, useRef, useEffect, useLayoutEffect, useCallback } from 'react'
import { useVirtualizer } from '@tanstack/react-virtual'
import { IconChevronDown } from '@tabler/icons-react'
import { useStore } from '../../store'
import { useSessionWS } from '../../hooks/useSessionWS'
import { useVoiceInput } from '../../hooks/useVoiceInput'
import { tts } from '../../hooks/useTTS'
import { getVoicePrefs, saveVoicePrefs } from '../../lib/voicePrefs'
import { useTTSReadCache } from '../../hooks/useTTSReadCache'
import { MessageBubble } from './MessageBubble'
import { ThinkingIndicator } from './ThinkingIndicator'
import { InputControls } from './InputControls'
import { VoiceSettingsModal } from './VoiceBar'
import { IdeasModal } from './IdeasModal'
import { FilePreviewPanel } from './FilePreviewPanel'
import { StarredPanel } from './StarredPanel'
import { fetchIdeas } from '../../lib/ideasStorage'
import * as api from '../../api'
import type { Message } from '../../types'

// Stable references — never inline [] or {} in Zustand selectors,
// they create a new reference every render → "getSnapshot should be cached" infinite loop.
const EMPTY_MESSAGES: Message[] = []


export function ChatView() {
  const activeSessionId = useStore(s => s.activeSessionId)
  const pendingSession = useStore(s => s.pendingSession)
  const messages = useStore(s =>
    activeSessionId ? (s.messages[activeSessionId] ?? EMPTY_MESSAGES) : EMPTY_MESSAGES
  )
  const pagination = useStore(s =>
    activeSessionId ? s.messagePagination[activeSessionId] : undefined
  )
  const streamingText = useStore(s => activeSessionId ? (s.streamingText[activeSessionId] ?? '') : '')
  const streamingThinking = useStore(s => activeSessionId ? (s.streamingThinking[activeSessionId] ?? '') : '')
  const sessionStatus = useStore(s =>
    activeSessionId ? (s.sessionStatus[activeSessionId] ?? 'idle') : 'idle'
  )
  const sessionUsage = useStore(s => activeSessionId ? s.sessionUsage[activeSessionId] : undefined)
  const activeProjectId = useStore(s => s.activeProjectId)
  const sessions = useStore(s => s.sessions)
  const sendMessage = useStore(s => s.sendMessage)
  const loadMoreMessages = useStore(s => s.loadMoreMessages)
  const filePreview = useStore(s => s.filePreview)
  const starredPanelOpen = useStore(s => s.starredPanelOpen)
  const openStarredPanel = useStore(s => s.openStarredPanel)
  const closeStarredPanel = useStore(s => s.closeStarredPanel)
  const toggleStar = useStore(s => s.toggleStar)
  const loadStarredMessages = useStore(s => s.loadStarredMessages)
  const loadMessageContext = useStore(s => s.loadMessageContext)
  const starredMessages = useStore(s =>
    activeSessionId ? (s.starredMessages[activeSessionId] ?? EMPTY_MESSAGES) : EMPTY_MESSAGES
  )

  const [input, setInput] = useState('')
  const [model, setModel] = useState('haiku')
  const [effort, setEffort] = useState('auto')
  const [thinking, setThinking] = useState('adaptive')
  const [attachedImage, setAttachedImage] = useState<{ base64: string; mediaType: string } | null>(null)

  // Voice input and TTS
  const [voiceLang, setVoiceLang] = useState(() => getVoicePrefs().lang)
  const [autoSend, setAutoSend] = useState(() => getVoicePrefs().autoSend)
  const [autoRead, setAutoRead] = useState(() => getVoicePrefs().autoRead)
  const [noEcho, setNoEcho] = useState(() => getVoicePrefs().noEcho)
  const [voiceURI, setVoiceURI] = useState(() => getVoicePrefs().voiceURI)
  const { isRead, markRead, loaded: ttsDbLoaded } = useTTSReadCache()
  const [interimText, setInterimText] = useState('')
  const [voiceSettingsOpen, setVoiceSettingsOpen] = useState(false)
  const [ideasOpen, setIdeasOpen] = useState(false)
  const [pendingIdeasCount, setPendingIdeasCount] = useState(0)
  const [speakingMessageId, setSpeakingMessageId] = useState<string | null>(null)

  const scrollRef = useRef<HTMLDivElement>(null)
  const bottomRef = useRef<HTMLDivElement>(null)
  const isAtBottomRef = useRef(true)
  const prevScrollHeightRef = useRef(0)
  const isPrependingRef = useRef(false)
  const isLoadingMoreRef = useRef(false)

  // Load pending ideas count (both scopes) when project changes
  useEffect(() => {
    let cancelled = false
    async function load() {
      const scopes: string[] = ['global']
      if (activeProjectId) scopes.push(activeProjectId)
      const results = await Promise.all(scopes.map(s => fetchIdeas(s).catch(() => [])))
      if (!cancelled) {
        const pending = results.flat().filter(i => !i.done).length
        setPendingIdeasCount(pending)
      }
    }
    load()
    return () => { cancelled = true }
  }, [activeProjectId])

  // Open WebSocket connection for the active session
  const { sendHookRespond } = useSessionWS(activeSessionId)

  // isRunning ref — available in voice callbacks without stale closure issues
  const isRunningRef = useRef(false)

  // Refs for values needed inside voice callbacks (avoid stale closures)
  const noEchoRef = useRef(noEcho)
  noEchoRef.current = noEcho
  const speakingMessageIdRef = useRef<string | null>(null)

  // Voice input
  const { isListening, start: startListening, stop: stopListening } = useVoiceInput({
    lang: voiceLang,
    onTranscript: (text) => {
      // Voice commands — consumed silently, never sent to chat
      const normalized = text.trim().toLowerCase().replace(/[.,!?;:]+$/, '')
      if (/^(stop|detener|detente)$/.test(normalized)) {
        if (speakingMessageIdRef.current) {
          tts.stop()
          setSpeakingMessageId(null)
          speakingMessageIdRef.current = null
        }
        return
      }

      // No-echo: coherent speech detected — stop TTS if playing
      if (noEchoRef.current && speakingMessageIdRef.current) {
        tts.stop()
        setSpeakingMessageId(null)
        speakingMessageIdRef.current = null
        return // discard the transcript that was picking up TTS audio
      }

      setInput((prev) => prev + (prev ? ' ' : '') + text)
      setInterimText('')
      // Auto-send only if not already running a turn (prevents double-turn)
      if (autoSend && activeSessionId && !isRunningRef.current) {
        setTimeout(() => {
          const newInput = input + (input ? ' ' : '') + text
          if (newInput.trim()) {
            const targetId = activeSessionId || 'pending'
            sendMessage(targetId, newInput.trim(), model, effort, thinking, attachedImage)
            setInput('')
            setAttachedImage(null)
          }
        }, 0)
      }
    },
    onInterim: (text) => {
      setInterimText(text)
    },
  })


  const session = sessions.find(s => s.id === activeSessionId)
  const isRunning = sessionStatus === 'running' || sessionStatus === 'waiting_hook'
  isRunningRef.current = isRunning
  const hasStreamingText = streamingText.length > 0
  const hasStreamingThinking = streamingThinking.length > 0
  const hasMore = pagination?.hasMore ?? false
  const isLoadingMore = pagination?.isLoadingMore ?? false
  const isPending = !activeSessionId && pendingSession !== null

  // Virtual list
  const virtualizer = useVirtualizer({
    count: messages.length,
    getScrollElement: () => scrollRef.current,
    estimateSize: () => 80,
    overscan: 8,
  })

  // Auto-scroll to bottom when new messages arrive (only if already at bottom)
  useEffect(() => {
    if (isAtBottomRef.current) {
      bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
    }
  }, [messages.length, streamingText])

  // Preserve scroll position after prepend (runs synchronously before paint)
  useLayoutEffect(() => {
    if (isPrependingRef.current && scrollRef.current) {
      const el = scrollRef.current
      el.scrollTop += el.scrollHeight - prevScrollHeightRef.current
      isPrependingRef.current = false
    }
  }, [messages.length])

  // Reset to bottom when switching sessions
  useEffect(() => {
    isAtBottomRef.current = true
    isLoadingMoreRef.current = false
  }, [activeSessionId])

  // Stop TTS as soon as voice input is detected (interim = browser heard something)
  useEffect(() => {
    if (interimText && speakingMessageIdRef.current) {
      tts.stop()
      setSpeakingMessageId(null)
      speakingMessageIdRef.current = null
    }
  }, [interimText])

  // Auto-read new assistant messages — skips messages already read (persisted in IndexedDB).
  // Manual "Read aloud" always works regardless of read status.
  useEffect(() => {
    if (!autoRead || !ttsDbLoaded || messages.length === 0) return

    const lastMessage = messages[messages.length - 1]
    if (
      lastMessage?.role === 'assistant' &&
      !isRead(lastMessage.id) &&
      !streamingText
    ) {
      markRead(lastMessage.id)

      // No-echo (speakers): pause mic while TTS plays, restore after
      const wasListening = isListening
      if (wasListening && !noEchoRef.current) stopListening()

      speakingMessageIdRef.current = lastMessage.id
      setSpeakingMessageId(lastMessage.id)

      tts.speak(lastMessage.content, voiceLang, voiceURI, {
        onEnd: () => {
          speakingMessageIdRef.current = null
          setSpeakingMessageId(null)
          if (wasListening && !noEchoRef.current) startListening()
        },
      })
    }
  }, [messages, autoRead, streamingText, voiceLang, ttsDbLoaded]) // eslint-disable-line react-hooks/exhaustive-deps

  const triggerLoadMore = useCallback(async () => {
    if (!activeSessionId || isLoadingMoreRef.current) return
    isLoadingMoreRef.current = true
    prevScrollHeightRef.current = scrollRef.current?.scrollHeight ?? 0
    isPrependingRef.current = true
    await loadMoreMessages(activeSessionId)
    isLoadingMoreRef.current = false
  }, [activeSessionId, loadMoreMessages])

  const handleScroll = useCallback(() => {
    const el = scrollRef.current
    if (!el) return

    const distFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight
    isAtBottomRef.current = distFromBottom < 150

    if (el.scrollTop < 200 && hasMore && !isLoadingMoreRef.current) {
      triggerLoadMore()
    }
  }, [hasMore, triggerLoadMore])

  const handleSend = () => {
    if (!input.trim() || (!activeSessionId && !pendingSession) || isRunning) return
    // For pending sessions, we pass a placeholder ID; sendMessage handles the actual creation
    const targetId = activeSessionId || 'pending'
    sendMessage(targetId, input.trim(), model, effort, thinking, attachedImage)
    setInput('')
    setAttachedImage(null)
  }

  const handleKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      handleSend()
    }
  }

  const handlePaste = async (e: React.ClipboardEvent<HTMLTextAreaElement>) => {
    const items = e.clipboardData?.items
    if (!items) return

    for (const item of items) {
      if (item.type.startsWith('image/')) {
        e.preventDefault()
        const file = item.getAsFile()
        if (!file) continue

        const validTypes = ['image/jpeg', 'image/png', 'image/gif', 'image/webp']
        if (!validTypes.includes(file.type)) {
          alert('Only JPEG, PNG, GIF, and WebP images are supported')
          return
        }

        const reader = new FileReader()
        reader.onload = (event) => {
          const base64 = (event.target?.result as string).split(',')[1]
          setAttachedImage({ base64, mediaType: file.type })
        }
        reader.readAsDataURL(file)
        break
      }
    }
  }

  const handleVoiceLangChange = (lang: string) => {
    setVoiceLang(lang)
    setVoiceURI(null)
    saveVoicePrefs({ lang, voiceURI: null })
  }

  const handleVoiceURIChange = (uri: string | null) => {
    setVoiceURI(uri)
    saveVoicePrefs({ voiceURI: uri })
  }

  const handleAutoSendToggle = () => {
    const newValue = !autoSend
    setAutoSend(newValue)
    saveVoicePrefs({ autoSend: newValue })
  }

  const handleAutoReadToggle = () => {
    const newValue = !autoRead
    setAutoRead(newValue)
    saveVoicePrefs({ autoRead: newValue })
  }

  const handleNoEchoToggle = () => {
    const newValue = !noEcho
    setNoEcho(newValue)
    saveVoicePrefs({ noEcho: newValue })
  }

  const handleMicToggle = () => {
    // No-echo mode: clicking mic while TTS plays stops the audio
    if (noEcho && speakingMessageId) {
      tts.stop()
      setSpeakingMessageId(null)
      speakingMessageIdRef.current = null
      return
    }
    if (isListening) {
      stopListening()
    } else {
      startListening()
    }
  }

  const handleStopSpeaking = () => {
    tts.stop()
    setSpeakingMessageId(null)
    speakingMessageIdRef.current = null
  }

  const handleSpeakMessage = (messageId: string, content: string) => {
    // Stop any current playback first
    tts.stop()
    setSpeakingMessageId(messageId)
    speakingMessageIdRef.current = messageId
    tts.speak(content, voiceLang, voiceURI, {
      onEnd: () => {
        setSpeakingMessageId(null)
        speakingMessageIdRef.current = null
      },
    })
  }

  if (!activeSessionId && !pendingSession) {
    return (
      <div className="chat chat--empty">
        <div className="chat__empty-state">
          <div className="chat__empty-icon">◈</div>
          <p>Select or create a session to begin</p>
        </div>
      </div>
    )
  }

  const handleScrollToMessage = useCallback((messageId: string) => {
    const index = messages.findIndex(m => m.id === messageId)

    // If message not found, fetch context around it
    if (index === -1) {
      if (!activeSessionId) return

      loadMessageContext(messageId, activeSessionId).then(() => {
        // After loading context, try to find and scroll to the message
        setTimeout(() => {
          const allMessages = useStore.getState().messages[activeSessionId] ?? []
          const newIndex = allMessages.findIndex(m => m.id === messageId)
          if (newIndex !== -1) {
            virtualizer.scrollToIndex(newIndex, { align: 'center', behavior: 'smooth' })
          }
        }, 0)
      })
      return
    }

    virtualizer.scrollToIndex(index, { align: 'center', behavior: 'smooth' })
  }, [messages, virtualizer, activeSessionId, loadMessageContext])

  const virtualItems = virtualizer.getVirtualItems()

  const headerName = isPending ? 'New session' : (session?.name || 'Session')

  return (
    <div className={`chat${filePreview ? ' chat--preview-open' : ''}`}>
      <div className="chat__header">
        <span className="chat__session-name">{headerName}</span>
        <button
          className={`chat__starred-caret${starredPanelOpen ? ' chat__starred-caret--open' : ''}`}
          onClick={() => {
            if (starredPanelOpen) {
              closeStarredPanel()
            } else {
              if (activeSessionId) {
                loadStarredMessages(activeSessionId)
              }
              openStarredPanel()
            }
          }}
          aria-label={starredPanelOpen ? 'Close starred messages' : 'View starred messages'}
          title="Starred messages"
        >
          <IconChevronDown size={14} />
        </button>
        <div className="chat__header-right">
          {sessionUsage && (
            <span className="chat__usage-total">
              ↑{fmt(sessionUsage.input_tokens)} ↓{fmt(sessionUsage.output_tokens)} tokens
            </span>
          )}
          <span className={`chat__status chat__status--${sessionStatus}`}>
            {sessionStatus === 'running' && 'thinking…'}
            {sessionStatus === 'waiting_hook' && 'awaiting approval'}
            {isPending && 'ready'}
            {!isPending && sessionStatus === 'idle' && 'ready'}
            {!isPending && sessionStatus === 'error' && 'error'}
          </span>
        </div>
      </div>

      <div className="chat__messages" ref={scrollRef} onScroll={handleScroll}>
        {/* Load-more indicator */}
        {isLoadingMore && (
          <div className="chat__loading-more">Loading older messages…</div>
        )}
        {!isLoadingMore && hasMore && (
          <div className="chat__has-more" />
        )}

        {/* Virtual list */}
        <div style={{ height: `${virtualizer.getTotalSize()}px`, position: 'relative', width: '100%', maxWidth: '900px', margin: '0 auto' }}>
          {virtualItems.map(virtualItem => (
            <div
              key={virtualItem.key}
              data-index={virtualItem.index}
              ref={virtualizer.measureElement}
              style={{
                position: 'absolute',
                top: 0,
                transform: `translateY(${virtualItem.start}px)`,
                width: '100%',
                paddingBottom: '12px',
                boxSizing: 'border-box',
              }}
            >
              <MessageBubble
                message={messages[virtualItem.index]}
                sendHookRespond={sendHookRespond}
                isBeingRead={speakingMessageId === messages[virtualItem.index]?.id}
                onSpeak={handleSpeakMessage}
                onStopSpeaking={handleStopSpeaking}
                isStarred={messages[virtualItem.index]?.starred}
                onStar={toggleStar}
              />
            </div>
          ))}
        </div>

        {/* Live streaming thinking */}
        {hasStreamingThinking && (
          <div className="msg msg--thinking" style={{ marginTop: '12px' }}>
            <div className="msg__bubble msg__bubble--thinking">
              <details open>
                <summary className="msg__thinking-summary">Thinking…</summary>
                <p className="msg__text msg__text--streaming msg__text--thinking">{streamingThinking}</p>
              </details>
            </div>
          </div>
        )}

        {/* Live streaming text */}
        {hasStreamingText && (
          <div className="msg msg--assistant" style={{ marginTop: hasStreamingThinking ? '12px' : '12px' }}>
            <div className="msg__bubble">
              <p className="msg__text msg__text--streaming">{streamingText}</p>
            </div>
          </div>
        )}

        {isRunning && !hasStreamingText && !hasStreamingThinking && <ThinkingIndicator />}
        <div ref={bottomRef} />
      </div>

      <div className="chat__input-area">
        {attachedImage && (
          <div className="chat__image-preview">
            <img src={`data:${attachedImage.mediaType};base64,${attachedImage.base64}`} alt="attached" />
            <button
              className="chat__image-remove"
              onClick={() => setAttachedImage(null)}
              title="Remove image"
            >
              ✕
            </button>
          </div>
        )}
        <div className="chat__input-wrap">
          <textarea
            className="chat__input"
            placeholder={
              isRunning
                ? 'Claude is thinking…'
                : interimText
                  ? interimText
                  : 'Message Claude… (paste images here)'
            }
            value={input}
            onChange={e => setInput(e.target.value)}
            onKeyDown={handleKeyDown}
            onPaste={handlePaste}
            disabled={isRunning}
            rows={1}
          />
          <InputControls
            model={model}
            onModelChange={setModel}
            effort={effort}
            onEffortChange={setEffort}
            thinking={thinking}
            onThinkingChange={setThinking}
            onImageSelect={(base64, mediaType) => setAttachedImage({ base64, mediaType })}
            onSend={handleSend}
            isRunning={isRunning}
            canSend={input.trim().length > 0}
            onMicToggle={handleMicToggle}
            isListening={isListening}
            onSettingsClick={() => setVoiceSettingsOpen(true)}
            onIdeasClick={() => setIdeasOpen(true)}
            hasPendingIdeas={pendingIdeasCount > 0}
          />
        </div>
      </div>

      {/* Ideas Modal */}
      <IdeasModal
        isOpen={ideasOpen}
        projectId={activeProjectId}
        onClose={() => setIdeasOpen(false)}
        onPendingChange={setPendingIdeasCount}
      />

      {/* Voice Settings Modal */}
      <VoiceSettingsModal
        isOpen={voiceSettingsOpen}
        lang={voiceLang}
        autoSend={autoSend}
        autoRead={autoRead}
        noEcho={noEcho}
        voiceURI={voiceURI}
        onClose={() => setVoiceSettingsOpen(false)}
        onLangChange={handleVoiceLangChange}
        onAutoSendToggle={handleAutoSendToggle}
        onAutoReadToggle={handleAutoReadToggle}
        onNoEchoToggle={handleNoEchoToggle}
        onVoiceChange={handleVoiceURIChange}
      />

      {/* File preview panel — slides in from right, full chat height */}
      <FilePreviewPanel />

      {/* Starred messages panel — slides in from right */}
      <StarredPanel
        isOpen={starredPanelOpen}
        messages={starredMessages}
        onClose={closeStarredPanel}
        onUnstar={toggleStar}
        onSelectMessage={handleScrollToMessage}
      />
    </div>
  )
}

function fmt(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n)
}
