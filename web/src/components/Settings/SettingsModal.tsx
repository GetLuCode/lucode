import { IconChevronRight, IconX } from '@tabler/icons-react'
import { motion } from 'framer-motion'
import { useState } from 'react'
import { ToolsTab } from './ToolsTab'
import { SpacesTab } from './SpacesTab'
import { PermissionsTab } from './PermissionsTab'
import { PluginsTab } from '../Plugins/PluginsTab'
import './SettingsModal.css'
import { backdropClose } from '../../lib/backdropClose'

interface Props {
  isOpen: boolean
  onClose: () => void
  initialTab?: Tab
}

type Tab = 'tools' | 'spaces' | 'permissions' | 'plugins'

const TAB_LABELS: Record<Tab, string> = {
  tools: 'Voice Tools',
  spaces: 'Spaces',
  permissions: 'Permissions',
  plugins: 'Plugins',
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
      {...backdropClose(onClose)}
    >
      <motion.div
        className={`settings-modal${tab === 'plugins' ? ' settings-modal--wide' : ''}`}
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
          <button
            className={`settings-modal__tab${tab === 'permissions' ? ' settings-modal__tab--active' : ''}`}
            onClick={() => setTab('permissions')}
          >
            Permissions
          </button>
          <button
            className={`settings-modal__tab${tab === 'plugins' ? ' settings-modal__tab--active' : ''}`}
            onClick={() => setTab('plugins')}
          >
            Plugins
          </button>
        </div>

        <div className="settings-modal__body">
          {tab === 'tools' && <ToolsTab />}
          {tab === 'spaces' && <SpacesTab />}
          {tab === 'permissions' && <PermissionsTab />}
          {tab === 'plugins' && <PluginsTab />}
        </div>
      </motion.div>
    </motion.div>
  )
}
