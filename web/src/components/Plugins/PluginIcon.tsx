import { useEffect, useState, type ReactNode } from 'react'
import { IconPuzzle } from '@tabler/icons-react'
import * as api from '../../api'
import { isSafeAssetType, safeAssetPath } from '../../lib/plugins'
import { usePlugins } from '../../store/plugins'

// A plugin's image. The route needs the token, so it is fetched as a blob and drawn through an
// object URL in an <img>, where an svg runs nothing. It is never inserted into the page as markup.
const cache = new Map<string, Promise<string | null>>()

function assetUrl(pluginId: string, encodedPath: string, version: number): Promise<string | null> {
  const key = `${version}:${pluginId}/${encodedPath}`
  let hit = cache.get(key)
  if (!hit) {
    hit = api.fetchPluginAsset(pluginId, encodedPath)
      .then(blob => (isSafeAssetType(blob.type) ? URL.createObjectURL(blob) : null))
      .catch(() => null)
    cache.set(key, hit)
  }
  return hit
}

export function PluginIcon({ pluginId, path, size = 16, className, alt = '', fallback }: {
  pluginId: string
  path?: string
  size?: number
  className?: string
  alt?: string
  fallback?: ReactNode
}) {
  const version = usePlugins(s => s.version)
  const encoded = safeAssetPath(path)
  const [url, setUrl] = useState<string | null>(null)
  useEffect(() => {
    if (!encoded) { setUrl(null); return }
    let cancelled = false
    void assetUrl(pluginId, encoded, version).then(u => { if (!cancelled) setUrl(u) })
    return () => { cancelled = true }
  }, [pluginId, encoded, version])
  if (url) return <img src={url} width={size} height={size} alt={alt} className={className} draggable={false} style={{ objectFit: 'contain' }} />
  return <>{fallback ?? <IconPuzzle size={size} className={className} aria-hidden={!alt} />}</>
}

// Who is asking. Host-drawn, around anything a plugin shows or asks for: the words outside the name
// are lucode's, so a plugin cannot pass for the app.
export function PluginOrigin({ pluginId, className }: { pluginId: string; className?: string }) {
  const name = usePlugins(s => s.installed.find(p => p.manifest?.id === pluginId)?.manifest.name)
  return (
    <span className={`plugin-origin${className ? ` ${className}` : ''}`}>
      <InstalledPluginIcon pluginId={pluginId} size={13} />
      <span><span className="plugin-origin__from">From the plugin </span><strong>{name || pluginId}</strong></span>
    </span>
  )
}

// The icon of an installed plugin, by id, for the places that only know the id.
export function InstalledPluginIcon({ pluginId, size = 16, className }: { pluginId: string; size?: number; className?: string }) {
  const icon = usePlugins(s => s.installed.find(p => p.manifest?.id === pluginId)?.manifest.icon)
  return <PluginIcon pluginId={pluginId} path={icon} size={size} className={className} />
}
