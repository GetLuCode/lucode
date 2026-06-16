import { IconBulb } from '@tabler/icons-react'

interface Props {
  model: string
  thinking: string
  onThinkingChange: (thinking: string) => void
  disabled: boolean
}

const THINKING_STATES = ['disabled', 'adaptive'] as const

export function ThinkingToggle({ model, thinking, onThinkingChange, disabled }: Props) {
  const isM3 = model.includes('M3')

  const handleClick = () => {
    const idx = THINKING_STATES.indexOf(thinking as typeof THINKING_STATES[number])
    const next = THINKING_STATES[(idx + 1) % THINKING_STATES.length]
    onThinkingChange(next)
  }

  const tooltip = isM3
    ? `Thinking: ${thinking}`
    : 'Thinking: always on (M2.7)'

  return (
    <div className="tooltip-wrapper">
      <button
        className={`thinking-toggle thinking-toggle--${isM3 ? thinking : 'adaptive'}`}
        onClick={handleClick}
        disabled={disabled || !isM3}
        title={tooltip}
      >
        <IconBulb size={18} />
      </button>
      <div className="tooltip">{tooltip}</div>
    </div>
  )
}
