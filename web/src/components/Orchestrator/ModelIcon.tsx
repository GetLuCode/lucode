import { IconSparkles } from '@tabler/icons-react'
import { usePlugins } from '../../store/plugins'
import { isPluginModel, normalizeModelId } from '../../lib/plugins'
import { PluginIcon } from '../Plugins/PluginIcon'

// The mark of the model in use: Claude's orange burst, or the icon a plugin's manifest gives its
// model (fetched with the token and drawn as an image, never as inline SVG), in its accent color.
export function ModelIcon({ model, size = 16, className }: { model: string; size?: number; className?: string }) {
  const id = normalizeModelId(model)
  const plugin = usePlugins(s => s.ui.models.find(m => m.value === id))
  if (isPluginModel(id)) {
    const fallback = <IconSparkles size={size} className={className} color={plugin?.accent ?? 'var(--text-muted)'} role="img" aria-label={plugin?.label ?? 'Plugin model'} />
    return plugin?.icon
      ? <PluginIcon pluginId={plugin.pluginId} path={plugin.icon} size={size} className={className} alt={plugin.label} fallback={fallback} />
      : fallback
  }
  const rays = Array.from({ length: 10 }, (_, i) => i * 36)
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" className={className} role="img" aria-label="Claude">
      <g stroke="#D97757" strokeWidth="2.3" strokeLinecap="round">
        {rays.map((deg, i) => (
          <line key={deg} x1="12" y1={i % 2 ? 6.5 : 4.5} x2="12" y2="10" transform={`rotate(${deg} 12 12)`} />
        ))}
      </g>
    </svg>
  )
}
