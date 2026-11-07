import { PluginsTab } from './PluginsTab'
import './Plugins.css'

// /projects/:projectId/plugins: the installed plugins, and where to install more. The same list as
// Settings → Plugins, in the project's tabs.
export function PluginsPage() {
  return (
    <div className="plugins-page">
      <div className="plugins-page__inner">
        <PluginsTab />
      </div>
    </div>
  )
}
