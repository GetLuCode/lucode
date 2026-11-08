import { useEffect } from 'react'
import { AnimatePresence, motion } from 'framer-motion'
import { IconAlertTriangle, IconCheck, IconInfoCircle, IconX } from '@tabler/icons-react'
import { useStore } from '../../store'
import { usePlugins } from '../../store/plugins'
import { InstalledPluginIcon, PluginOrigin } from './PluginIcon'
import { PluginDialog } from './PluginDialog'
import { ViewRenderer } from './ViewRenderer'
import './Plugins.css'

// Mounted once in the app: loads the plugins, and shows what their actions answer.
export function PluginHost() {
  const load = usePlugins(s => s.load)
  const available = usePlugins(s => s.available)
  const remote = useStore(s => s.activeRemoteId)
  const toasts = usePlugins(s => s.toasts)
  const dismiss = usePlugins(s => s.dismissToast)
  const result = usePlugins(s => s.result)
  const closeResult = usePlugins(s => s.closeResult)
  const projectId = useStore(s => s.activeProjectId)
  const installed = usePlugins(s => s.installed)
  const pluginName = (id: string) => installed.find(p => p.manifest?.id === id)?.manifest.name ?? id

  useEffect(() => { void load() }, [load, remote])
  // A hub without the plugin routes may gain them when it restarts: look again on coming back.
  useEffect(() => {
    if (available) return
    const again = () => { if (document.visibilityState === 'visible') void load() }
    window.addEventListener('focus', again)
    return () => window.removeEventListener('focus', again)
  }, [available, load])

  return (
    <>
      <div className="plugin-toasts" aria-live="polite">
        <AnimatePresence initial={false}>
          {toasts.map(t => (
            <motion.div
              key={t.id}
              layout
              className={`plugin-toast plugin-toast--${t.tone}`}
              role={t.tone === 'danger' ? 'alert' : 'status'}
              initial={{ opacity: 0, y: 12 }}
              animate={{ opacity: 1, y: 0 }}
              exit={{ opacity: 0, y: 6 }}
              transition={{ duration: 0.18, ease: [0.16, 1, 0.3, 1] }}
            >
              {t.tone === 'danger' || t.tone === 'warning'
                ? <IconAlertTriangle size={15} aria-hidden />
                : t.tone === 'success' ? <IconCheck size={15} aria-hidden />
                : t.pluginId ? <InstalledPluginIcon pluginId={t.pluginId} size={15} />
                : <IconInfoCircle size={15} aria-hidden />}
              <span>{t.pluginId && <strong className="plugin-toast__who">{pluginName(t.pluginId)}: </strong>}{t.text}</span>
              <button className="plugin-icon-btn" onClick={() => dismiss(t.id)} aria-label="Dismiss"><IconX size={13} /></button>
            </motion.div>
          ))}
        </AnimatePresence>
      </div>
      {result && (
        <PluginDialog
          wide
          title={result.title}
          icon={<InstalledPluginIcon pluginId={result.pluginId} size={18} />}
          subtitle={<PluginOrigin pluginId={result.pluginId} />}
          onClose={closeResult}
        >
          <ViewRenderer
            pluginId={result.pluginId}
            projectId={projectId}
            tree={result.tree}
            onTree={tree => usePlugins.setState(s => (s.result ? { result: { ...s.result, tree } } : s))}
          />
        </PluginDialog>
      )}
    </>
  )
}
