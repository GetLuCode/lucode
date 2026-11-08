import { useEffect, useMemo, useState } from 'react'
import type { ComponentType } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { IconKey, IconMicrophone, IconPlug, IconPuzzle, IconServer2, IconX } from '@tabler/icons-react'
import type { Icon } from '@tabler/icons-react'
import { ToolsTab } from './ToolsTab'
import { SpacesTab } from './SpacesTab'
import { PermissionsTab } from './PermissionsTab'
import { PluginsTab, PluginSettingsPage, pluginSettingsPath } from '../Plugins/PluginsTab'
import { usePlugins } from '../../store/plugins'
import './SettingsTabs.css'
import './SettingsPage.css'

interface Section {
  id: string
  label: string
  description: string
  icon: Icon
  Component?: ComponentType
  pluginId?: string // a plugin's own page: rendered by PluginSettingsPage
}

interface Group {
  label: string
  sections: Section[]
}

// Add a section here and it shows up in the menu, the URL and the header.
const GROUPS: Group[] = [
  {
    label: 'Workspace',
    sections: [
      { id: 'spaces', label: 'Instances', description: 'Hubs and runners this hub can see on the network.', icon: IconServer2, Component: SpacesTab },
    ],
  },
  {
    label: 'Agents',
    sections: [
      { id: 'permissions', label: 'Permissions', description: 'Which tools an agent may use without asking.', icon: IconKey, Component: PermissionsTab },
      { id: 'tools', label: 'Voice Tools', description: 'Tools the voice assistant can call for you.', icon: IconMicrophone, Component: ToolsTab },
    ],
  },
  {
    label: 'Extensions',
    sections: [
      { id: 'plugins', label: 'Plugins', description: 'Installed plugins, where they run and what they may do.', icon: IconPuzzle, Component: PluginsTab },
    ],
  },
]

const STATIC = GROUPS.flatMap(g => g.sections)
const DEFAULT_SECTION = STATIC[0].id

// /settings/:section — an account-wide page (no project), like Usage. Left menu with groups,
// content on the right; Esc or the X goes back to where the user came from.
export function SettingsPage() {
  const { section, pluginId } = useParams<{ section?: string; pluginId?: string }>()
  const navigate = useNavigate()
  const location = useLocation()
  const from = (location.state as { from?: string } | null)?.from
  const [query, setQuery] = useState('')
  const installed = usePlugins(s => s.installed)
  const loadPlugins = usePlugins(s => s.load)
  useEffect(() => { void loadPlugins() }, [loadPlugins])

  // Every installed plugin gets its own settings page under Extensions.
  const pluginSections = useMemo<Section[]>(() => installed.map(p => ({
    id: `plugin/${p.manifest.id}`,
    label: p.manifest.name,
    description: p.manifest.description || 'Settings, runners, logs and details of this plugin.',
    icon: IconPlug,
    pluginId: p.manifest.id,
  })), [installed])

  const allGroups = useMemo<Group[]>(() => GROUPS.map(g => (
    g.label === 'Extensions' ? { ...g, sections: [...g.sections, ...pluginSections] } : g
  )), [pluginSections])
  const ALL = useMemo(() => allGroups.flatMap(g => g.sections), [allGroups])

  const wanted = pluginId ? `plugin/${pluginId}` : section
  const current: Section = ALL.find(s => s.id === wanted)
    ?? (pluginId
      ? { id: wanted!, label: 'Plugin', description: 'Settings, runners, logs and details of this plugin.', icon: IconPlug, pluginId }
      : STATIC[0])
  // A plugin page is valid while the list is still loading; an unknown static section is not.
  const unknownStatic = !!section && !pluginId && !ALL.some(s => s.id === section)

  const close = () => navigate(from && !from.startsWith('/settings') ? from : '/')

  useEffect(() => {
    if (unknownStatic) {
      navigate(`/settings/${DEFAULT_SECTION}`, { replace: true, state: location.state })
    }
  }, [unknownStatic, navigate, location.state])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape' || e.defaultPrevented) return
      const t = e.target as HTMLElement | null
      if (t && /^(INPUT|TEXTAREA|SELECT)$/.test(t.tagName)) return // Esc in a field is for the field
      if (document.querySelector('[role="dialog"]')) return // a dialog on top takes it first
      close()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [from])

  const groups = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return allGroups
    return allGroups
      .map(g => ({ ...g, sections: g.sections.filter(s => `${s.label} ${s.description}`.toLowerCase().includes(q)) }))
      .filter(g => g.sections.length > 0)
  }, [query, allGroups])

  const Body = current.Component

  return (
    <div className="settings-page">
      <nav className="settings-page__menu" aria-label="Settings sections">
        <input
          className="settings-page__search"
          type="search"
          placeholder="Search settings"
          value={query}
          onChange={e => setQuery(e.target.value)}
          aria-label="Search settings"
        />
        <div className="settings-page__groups">
          {groups.map(g => (
            <div className="settings-page__group" key={g.label}>
              <div className="settings-page__group-label">{g.label}</div>
              {g.sections.map(s => {
                const SIcon = s.icon
                const active = s.id === current.id
                return (
                  <button
                    key={s.id}
                    className={`settings-page__item${active ? ' settings-page__item--active' : ''}`}
                    aria-current={active ? 'page' : undefined}
                    onClick={() => navigate(s.pluginId ? pluginSettingsPath(s.pluginId) : `/settings/${s.id}`, { state: location.state })}
                  >
                    <SIcon size={16} />
                    <span>{s.label}</span>
                  </button>
                )
              })}
            </div>
          ))}
          {groups.length === 0 && <p className="settings-page__none">Nothing matches “{query}”.</p>}
        </div>
      </nav>

      <div className="settings-page__main">
        <header className="settings-page__header">
          <div>
            <h1 className="settings-page__title">{current.label}</h1>
            <p className="settings-page__desc">{current.description}</p>
          </div>
          <button className="settings-page__close" onClick={close} aria-label="Close settings" title="Close (Esc)">
            <IconX size={18} />
            <span>ESC</span>
          </button>
        </header>
        <div className="settings-page__scroll">
          <div className="settings-page__content">
            {current.pluginId
              ? <PluginSettingsPage key={current.id} pluginId={current.pluginId} />
              : Body && <Body key={current.id} />}
          </div>
        </div>
      </div>
    </div>
  )
}
