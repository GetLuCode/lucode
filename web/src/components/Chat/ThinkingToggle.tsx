import { IconBulb } from '@tabler/icons-react'

interface Props {
  thinking: string
  onThinkingChange: (thinking: string) => void
  disabled: boolean
}

export function ThinkingToggle({ thinking, onThinkingChange, disabled }: Props) {
  const handleClick = () => {
    const states = ['disabled', 'adaptive', 'enabled']
    const currentIndex = states.indexOf(thinking as string)
    const nextIndex = (currentIndex + 1) % states.length
    onThinkingChange(states[nextIndex])
  }

  const getTooltip = () => {
    switch (thinking) {
      case 'enabled':
        return 'Thinking: enabled'
      case 'disabled':
        return 'Thinking: disabled'
      case 'adaptive':
      default:
        return 'Thinking: adaptive'
    }
  }

  return (
    <div className="tooltip-wrapper">
      <button
        className={`thinking-toggle thinking-toggle--${thinking}`}
        onClick={handleClick}
        disabled={disabled}
        title={getTooltip()}
      >
        <IconBulb size={18} />
        {thinking === 'enabled' && <span className="thinking-toggle__label">Thinking</span>}
      </button>
      <div className="tooltip">{getTooltip()}</div>
    </div>
  )
}
