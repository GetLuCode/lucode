import { useState, useRef, useEffect } from 'react'
import { IconSettings, IconChevronRight } from '@tabler/icons-react'

interface Props {
  onVoiceSettingsClick: () => void
  disabled: boolean
}

export function SettingsMenu({ onVoiceSettingsClick, disabled }: Props) {
  const [isOpen, setIsOpen] = useState(false)
  const menuRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) {
        setIsOpen(false)
      }
    }

    if (isOpen) {
      document.addEventListener('mousedown', handleClickOutside)
      return () => document.removeEventListener('mousedown', handleClickOutside)
    }
  }, [isOpen])

  const handleVoiceClick = () => {
    onVoiceSettingsClick()
    setIsOpen(false)
  }

  return (
    <div className="settings-menu" ref={menuRef}>
      <div className="tooltip-wrapper">
        <button
          className="thinking-toggle settings-menu__button"
          onClick={() => setIsOpen(!isOpen)}
          disabled={disabled}
          title="Settings"
        >
          <IconSettings size={18} />
        </button>
        <div className="tooltip">Settings</div>
      </div>

      {isOpen && (
        <div className="settings-menu__popover">
          <button className="settings-menu__item" onClick={handleVoiceClick}>
            <span>Voice</span>
            <IconChevronRight size={14} />
          </button>
        </div>
      )}
    </div>
  )
}
