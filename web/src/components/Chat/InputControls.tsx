import { Listbox } from '@headlessui/react'
import { IconBrain } from '@tabler/icons-react'

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
  onSend: () => void
  isRunning: boolean
  canSend: boolean
}

export function InputControls({
  model,
  onModelChange,
  effort,
  onEffortChange,
  thinking,
  onThinkingChange,
  onSend,
  isRunning,
  canSend,
}: Props) {
  return (
    <div className="input-controls">
      {/* Left: Thinking toggle */}
      <div className="input-controls__group input-controls__group--left">
        <div className="tooltip-wrapper">
          <button
            className={`thinking-toggle ${thinking === 'enabled' ? 'thinking-toggle--active' : ''}`}
            onClick={() => onThinkingChange(thinking === 'enabled' ? 'disabled' : 'enabled')}
            disabled={isRunning}
            title={thinking === 'enabled' ? 'Disable thinking' : 'Enable thinking'}
          >
            <IconBrain size={18} />
          </button>
          <div className="tooltip">
            {thinking === 'enabled' ? 'Disable thinking' : 'Enable thinking'}
          </div>
        </div>
      </div>

      {/* Center/Right: Selects and send button */}
      <div className="input-controls__group input-controls__group--right">
        <Listbox value={effort} onChange={onEffortChange} disabled={isRunning}>
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
