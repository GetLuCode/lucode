import { IconGauge, IconMicrophone, IconMicrophoneOff, IconNotes, IconPlayerStop } from '@tabler/icons-react'
import { AttachmentButton } from './AttachmentButton'
import { ThinkingToggle } from './ThinkingToggle'
import { SettingsMenu } from './SettingsMenu'

const EFFORTS = ['auto', 'low', 'medium', 'high', 'max'] as const
type Effort = typeof EFFORTS[number]

const EFFORT_LABELS: Record<Effort, string> = {
  auto: 'Auto',
  low: 'Low',
  medium: 'Med',
  high: 'High',
  max: 'Max',
}

interface Props {
  model: string
  effort: string
  onEffortChange: (effort: string) => void
  thinking: boolean | string
  onThinkingChange: (thinking: string) => void
  onImageSelect: (base64: string, mediaType: string) => void
  onSend: () => void
  isRunning: boolean
  canSend: boolean
  onMicToggle?: () => void
  isListening?: boolean
  onSettingsClick?: () => void
  onIdeasClick?: () => void
  hasPendingIdeas?: boolean
  isSpeaking?: boolean
  onStopReading?: () => void
}

export function InputControls({
  model,
  effort,
  onEffortChange,
  thinking,
  onThinkingChange,
  onImageSelect,
  onSend,
  isRunning,
  canSend,
  onMicToggle = () => {},
  isListening = false,
  onSettingsClick = () => {},
  onIdeasClick = () => {},
  hasPendingIdeas = false,
  isSpeaking = false,
  onStopReading = () => {},
}: Props) {
  const handleEffortCycle = () => {
    const idx = EFFORTS.indexOf(effort as Effort)
    const next = EFFORTS[(idx + 1) % EFFORTS.length]
    onEffortChange(next)
  }

  const effortLabel = EFFORT_LABELS[effort as Effort] ?? effort
  const isNonDefaultEffort = effort !== 'auto'

  return (
    <div className="input-controls">
      {/* Left: tools */}
      <div className="input-controls__group input-controls__group--left">
        <AttachmentButton onImageSelect={onImageSelect} disabled={isRunning} />
        <ThinkingToggle thinking={String(thinking)} onThinkingChange={onThinkingChange} disabled={isRunning} />

        {/* Effort toggle — only for non-haiku models */}
        {model !== 'haiku' && (
          <div className="tooltip-wrapper">
            <button
              className={`thinking-toggle thinking-toggle--effort thinking-toggle--effort-${effort}`}
              onClick={handleEffortCycle}
              disabled={isRunning}
            >
              <IconGauge size={18} />
              {isNonDefaultEffort && <span className="thinking-toggle__label">{effortLabel}</span>}
            </button>
            <div className="tooltip">Effort: {effortLabel}</div>
          </div>
        )}

        {/* Ideas button */}
        <div className="tooltip-wrapper">
          <button
            className={`thinking-toggle ideas-btn ${hasPendingIdeas ? 'ideas-btn--pending' : ''}`}
            onClick={onIdeasClick}
            title={hasPendingIdeas ? 'Ideas (pending)' : 'Ideas'}
          >
            <IconNotes size={18} />
          </button>
          <div className="tooltip">{hasPendingIdeas ? 'Notes — pending items' : 'Notes'}</div>
        </div>

        {/* Settings menu */}
        <SettingsMenu onVoiceSettingsClick={onSettingsClick} disabled={isRunning} />

        {/* Stop reading button — blinking, visible only when TTS is active */}
        {isSpeaking && (
          <div className="tooltip-wrapper">
            <button
              className="thinking-toggle stop-reading-btn"
              onClick={onStopReading}
              title="Stop reading"
            >
              <IconPlayerStop size={18} />
            </button>
            <div className="tooltip">Stop reading</div>
          </div>
        )}
      </div>

      {/* Right: mic + send */}
      <div className="input-controls__group input-controls__group--right">
        <div className="tooltip-wrapper">
          <button
            className={`thinking-toggle ${isListening ? 'thinking-toggle--listening' : ''}`}
            onClick={onMicToggle}
            disabled={isRunning}
            title={isListening ? 'Stop recording' : 'Start voice input'}
          >
            {isListening ? <IconMicrophoneOff size={18} /> : <IconMicrophone size={18} />}
          </button>
          <div className="tooltip">
            {isListening ? 'Stop recording' : 'Start voice input'}
          </div>
        </div>

        <button
          className="btn btn--send"
          onClick={onSend}
          disabled={!canSend || isRunning}
          title="Send message (Enter)"
        >
          ↑
        </button>
      </div>
    </div>
  )
}
