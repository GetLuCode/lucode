import { useState } from 'react'
import { IconSearch, IconAdjustments, IconChartBar, IconLayoutSidebarLeftCollapse } from '@tabler/icons-react'
import { SettingsModal } from '../Settings/SettingsModal'

interface NavRailProps {
  activePage?: 'home' | 'usage'
  onOpenSearch?: () => void
  onNavigate?: (page: 'home' | 'usage') => void
}

export function NavRail({ activePage, onOpenSearch, onNavigate }: NavRailProps) {
  const [showSettings, setShowSettings] = useState(false)

  return (
    <div className="nav-panel nav-panel--collapsed nav-panel--rail-only">
      <div className="nav-panel__rail">
        <div className="nav-panel__rail-logo">
          <img src="/logo-icon.svg" alt="LuCode" width="20" height="20" />
        </div>
        <button
          className="nav-panel__rail-btn"
          onClick={() => onNavigate?.('home')}
          title="Back to projects"
        >
          <IconLayoutSidebarLeftCollapse size={17} />
        </button>
        <button
          className="nav-panel__rail-btn"
          onClick={onOpenSearch}
          title="Search (Ctrl+K)"
        >
          <IconSearch size={16} />
        </button>
        <button
          className={`nav-panel__rail-btn${activePage === 'usage' ? ' nav-panel__rail-btn--active' : ''}`}
          onClick={() => onNavigate?.('usage')}
          title="Usage metrics"
        >
          <IconChartBar size={16} />
        </button>
        <div className="nav-panel__rail-spacer" />
        <button
          className="nav-panel__rail-btn"
          title="Settings"
          onClick={() => setShowSettings(true)}
        >
          <IconAdjustments size={16} />
        </button>
        <SettingsModal isOpen={showSettings} onClose={() => setShowSettings(false)} />
      </div>
    </div>
  )
}
