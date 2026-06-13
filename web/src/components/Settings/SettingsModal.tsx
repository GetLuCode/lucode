import { IconChevronRight, IconX } from '@tabler/icons-react'
import { motion } from 'framer-motion'
import { useState } from 'react'
import { ToolsTab } from './ToolsTab'
import { SpacesTab } from './SpacesTab'
import './SettingsModal.css'

interface Props {
  isOpen: boolean
  onClose: () => void
  initialTab?: 'tools' | 'spaces'
}

type Tab = 'tools' | 'spaces'

const TAB_LABELS: Record<Tab, string> = {
  tools: 'Voice Tools',
  spaces: 'Spaces',
}

export function SettingsModal({ isOpen, onClose, initialTab }: Props) {
  const [tab, setTab] = useState<Tab>(initialTab ?? 'tools')

  if (!isOpen) return null

  return (
    <motion.div
      className="settings-modal-overlay"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      onClick={onClose}
    >
      <motion.div
        className="settings-modal"
        initial={{ scale: 0.95, opacity: 0 }}
        animate={{ scale: 1, opacity: 1 }}
        exit={{ scale: 0.95, opacity: 0 }}
        onClick={e => e.stopPropagation()}
      >
        <div className="settings-modal__header">
          <div className="settings-modal__breadcrumb">
            <span className="settings-modal__breadcrumb-root">Settings</span>
            <IconChevronRight size={13} className="settings-modal__breadcrumb-sep" />
            <span className="settings-modal__breadcrumb-current">{TAB_LABELS[tab]}</span>
          </div>
          <button className="settings-modal__close" onClick={onClose} aria-label="Close settings">
            <IconX size={18} />
          </button>
        </div>

        <div className="settings-modal__tabs">
          <button
            className={`settings-modal__tab${tab === 'tools' ? ' settings-modal__tab--active' : ''}`}
            onClick={() => setTab('tools')}
          >
            Voice Tools
          </button>
          <button
            className={`settings-modal__tab${tab === 'spaces' ? ' settings-modal__tab--active' : ''}`}
            onClick={() => setTab('spaces')}
          >
            Spaces
          </button>
        </div>

        <div className="settings-modal__body">
          {tab === 'tools' && <ToolsTab />}
          {tab === 'spaces' && <SpacesTab />}
        </div>
      </motion.div>
    </motion.div>
  )
}
