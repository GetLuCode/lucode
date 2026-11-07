import { useCallback, useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { IconAlertTriangle, IconPuzzle, IconRefresh } from '@tabler/icons-react'
import * as api from '../../api'
import { pluginErrorMessage } from '../../lib/plugins'
import { classifyRpcResult, type ViewNode } from '../../lib/pluginView'
import { useStore } from '../../store'
import { usePlugins } from '../../store/plugins'
import { PluginIcon, PluginOrigin } from './PluginIcon'
import { ViewEmpty, ViewRenderer } from './ViewRenderer'
import './Plugins.css'

// /projects/:projectId/plugins/:pluginId/:pageId: a page a plugin contributes, in the project's tabs.
export function PluginPage() {
  const { pluginId = '', pageId = '' } = useParams<{ pluginId: string; pageId: string }>()
  const projectId = useStore(s => s.activeProjectId)
  const page = usePlugins(s => s.ui.pages.find(p => p.pluginId === pluginId && p.id === pageId))
  const loaded = usePlugins(s => s.loaded)
  const [tree, setTree] = useState<ViewNode | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(true)
  const [attempt, setAttempt] = useState(0)
  // a webhook reached this plugin: read the page again, in place (what is already drawn stays until the answer)
  const webhookTick = usePlugins(s => s.webhookTick[pluginId] ?? 0)
  const render = page?.render

  useEffect(() => {
    if (!render || !projectId) return
    let cancelled = false
    setLoading(true)
    setError('')
    api.callPluginRpc(pluginId, render, projectId, {})
      .then(result => {
        if (cancelled) return
        const outcome = classifyRpcResult(result)
        setTree(outcome.kind === 'tree' ? outcome.tree : null)
        if (outcome.kind === 'toast') usePlugins.getState().toast(outcome.text, outcome.tone, pluginId)
      })
      .catch(e => { if (!cancelled) setError(pluginErrorMessage(e)) })
      .finally(() => { if (!cancelled) setLoading(false) })
    return () => { cancelled = true }
  }, [pluginId, render, projectId, attempt, webhookTick])

  const onTree = useCallback((t: ViewNode) => setTree(t), [])

  if (!page) {
    return (
      <div className="plugin-page plugin-page--center">
        {loaded
          ? <ViewEmpty title="This page is not available" text="Its plugin is off or was removed. Plugins are managed in Settings." />
          : <PageSkeleton />}
      </div>
    )
  }

  return (
    <div className={`plugin-page${tree?.type === 'conversation' ? ' plugin-page--fill' : ''}`}>
      {tree?.type !== 'conversation' && <header className="plugin-page__bar">
        <PluginIcon pluginId={pluginId} path={page.icon} size={18} fallback={<IconPuzzle size={18} stroke={1.6} />} />
        <h1 className="plugin-page__title">{page.label}</h1>
        <PluginOrigin pluginId={pluginId} className="plugin-page__origin" />
        <button
          className="plugin-icon-btn"
          onClick={() => setAttempt(a => a + 1)}
          disabled={loading}
          title="Reload this page"
          aria-label="Reload this page"
        >
          <IconRefresh size={15} className={loading ? 'plugin-spin' : undefined} />
        </button>
      </header>}
      <div className="plugin-page__body">
        {error ? (
          <div className="plugin-page__failure" role="alert">
            <IconAlertTriangle size={22} stroke={1.6} aria-hidden />
            <p className="pv-empty__title">This page could not load</p>
            <p className="pv-empty__text">{error}</p>
            <button className="pv-btn pv-btn--secondary" onClick={() => setAttempt(a => a + 1)}>Try again</button>
          </div>
        ) : tree ? (
          <ViewRenderer pluginId={pluginId} projectId={projectId} tree={tree} onTree={onTree} />
        ) : loading ? (
          <PageSkeleton />
        ) : (
          <ViewEmpty title="Nothing to show" text="This plugin returned no content for this page." />
        )}
      </div>
    </div>
  )
}

function PageSkeleton() {
  return (
    <div className="plugin-skeleton" aria-busy="true" aria-label="Loading">
      <div className="plugin-skeleton__line" style={{ width: '36%' }} />
      <div className="plugin-skeleton__row">
        <div className="plugin-skeleton__block" />
        <div className="plugin-skeleton__block" />
        <div className="plugin-skeleton__block" />
      </div>
      <div className="plugin-skeleton__line" style={{ width: '82%' }} />
      <div className="plugin-skeleton__line" style={{ width: '64%' }} />
    </div>
  )
}
