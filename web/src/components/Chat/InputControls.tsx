import { Listbox } from '@headlessui/react'
import { IconBulb, IconMicrophone, IconMicrophoneOff, IconNotes } from '@tabler/icons-react'
import { AttachmentButton } from './AttachmentButton'
import { ThinkingToggle } from './ThinkingToggle'
import { SettingsMenu } from './SettingsMenu'

const MODELS = [
  { label: 'Haiku 4.5', value: 'haiku' },
  { label: 'Sonnet 4.6', value: 'sonnet' },
  { label: 'Opus 4.6', value: 'opus' },
]

const EFFORTS = [
  { label: 'Auto', value: 'auto' },
  { label: 'Low', value: 'low' },
  { label: 'Medium', value: 'medium' },
  { label: 'High', value: 'high' },
  { label: 'Max', value: 'max' },
]

interface Props {
  model: string
  onModelChange: (model: string) => void
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
}

export function InputControls({
  model,
  onModelChange,
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
}: Props) {
  return (
    <div className="input-controls">
      {/* Left: Attachment and thinking toggle */}
      <div className="input-controls__group input-controls__group--left">
        <AttachmentButton onImageSelect={onImageSelect} disabled={isRunning} />
        <ThinkingToggle thinking={String(thinking)} onThinkingChange={onThinkingChange} disabled={isRunning} />

        {/* Voice controls group - mic button only */}
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
      </div>

      {/* Center/Right: Selects and send button */}
      <div className="input-controls__group input-controls__group--right">
        <div className={`labeled-select${model === 'haiku' || isRunning ? ' labeled-select--disabled' : ''}`}>
          <span className="labeled-select__label">Effort</span>
          <Listbox value={effort} onChange={onEffortChange} disabled={isRunning || model === 'haiku'}>
            <div className="listbox-wrapper">
              <Listbox.Button className="listbox-button">
                {EFFORTS.find(e => e.value === effort)?.label || 'Effort'}
                <span className="listbox-caret">▼</span>
              </Listbox.Button>
              <Listbox.Options className="listbox-options">
                {EFFORTS.map(e => (
                  <Listbox.Option
                    key={e.value}
                    value={e.value}
                    className={({ active, selected }) =>
                      `listbox-option ${active ? 'listbox-option--active' : ''} ${selected ? 'listbox-option--selected' : ''}`
                    }
                  >
                    {e.label}
                  </Listbox.Option>
                ))}
              </Listbox.Options>
            </div>
          </Listbox>
        </div>

        <div className={`labeled-select${isRunning ? ' labeled-select--disabled' : ''}`}>
          <span className="labeled-select__label">Model</span>
          <Listbox value={model} onChange={onModelChange} disabled={isRunning}>
            <div className="listbox-wrapper">
              <Listbox.Button className="listbox-button">
                {MODELS.find(m => m.value === model)?.label || 'Model'}
                <span className="listbox-caret">▼</span>
              </Listbox.Button>
              <Listbox.Options className="listbox-options">
                {MODELS.map(m => (
                  <Listbox.Option
                    key={m.value}
                    value={m.value}
                    className={({ active, selected }) =>
                      `listbox-option ${active ? 'listbox-option--active' : ''} ${selected ? 'listbox-option--selected' : ''}`
                    }
                  >
                    {m.label}
                  </Listbox.Option>
                ))}
              </Listbox.Options>
            </div>
          </Listbox>
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
