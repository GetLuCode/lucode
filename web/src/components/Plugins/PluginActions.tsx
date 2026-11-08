import { useState } from 'react'
import { IconLoader2, IconPuzzle } from '@tabler/icons-react'
import { useStore } from '../../store'
import { usePlugins } from '../../store/plugins'
import type { KanbanCard, Message, PluginActionContribution } from '../../types'
import { PluginIcon } from './PluginIcon'
import './Plugins.css'

// What plugins add to the row under an answer. Each one calls the plugin's rpc with the ids of the
// message; the answer comes back as a toast or a dialog.
export function PluginChatActions({ message }: { message: Message }) {
  const actions = usePlugins(s => s.ui.chatActions)
  const installed = usePlugins(s => s.installed)
  const pluginName = (id: string) => installed.find(p => p.manifest?.id === id)?.manifest.name ?? id
  const runAction = usePlugins(s => s.runAction)
  const projectId = useStore(s => s.activeProjectId)
  const [busy, setBusy] = useState('')
  if (actions.length === 0) return null

  const run = async (a: PluginActionContribution) => {
    const key = `${a.pluginId}/${a.id}`
    setBusy(key)
    try {
      await runAction(a.pluginId, a.rpc, projectId, { sessionId: message.session_id, messageId: message.id }, a.label)
    } finally {
      setBusy('')
    }
  }

  return (
    <>
      {actions.map(a => {
        const key = `${a.pluginId}/${a.id}`
        return (
          <div className="tooltip-wrapper" key={key}>
            <button className="msg__action-btn" onClick={() => run(a)} disabled={busy !== ''} aria-label={a.label}>
              {busy === key
                ? <IconLoader2 size={13} className="plugin-spin" />
                : <PluginIcon pluginId={a.pluginId} path={a.icon} size={13} fallback={<IconPuzzle size={13} />} />}
            </button>
            <div className="tooltip tooltip--up">{a.label} (plugin: {pluginName(a.pluginId)})</div>
          </div>
        )
      })}
    </>
  )
}

// What plugins add to a card's menu. The menu closes at once; the answer arrives on its own.
export function PluginCardActions({ card, onDone }: { card: Pick<KanbanCard, 'id' | 'project_id'>; onDone: () => void }) {
  const actions = usePlugins(s => s.ui.cardActions)
  const installed = usePlugins(s => s.installed)
  const pluginName = (id: string) => installed.find(p => p.manifest?.id === id)?.manifest.name ?? id
  const runAction = usePlugins(s => s.runAction)
  if (actions.length === 0) return null
  return (
    <>
      {actions.map(a => (
        <button
          key={`${a.pluginId}/${a.id}`}
          className="nav-tree__menu-item"
          onClick={() => {
            onDone()
            void runAction(a.pluginId, a.rpc, card.project_id, { cardId: card.id }, a.label)
          }}
        >
          <PluginIcon pluginId={a.pluginId} path={a.icon} size={14} fallback={<IconPuzzle size={14} />} />
          <span>{a.label} <small className="plugin-action__who">{pluginName(a.pluginId)}</small></span>
        </button>
      ))}
    </>
  )
}
