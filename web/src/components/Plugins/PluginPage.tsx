import { useCallback, useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { IconAlertTriangle, IconPuzzle, IconRefresh } from '@tabler/icons-react'
import * as api from '../../api'
import { pluginErrorMessage } from '../../lib/plugins'
import { classifyRpcResult, type ViewNode } from '../../lib/pluginView'
import { useStore } from '../../store'
import { usePlugins } from '../../store/plugins'
import type { PluginPageContribution } from '../../types'
import { PluginIcon, PluginOrigin } from './PluginIcon'
import { PluginSurface } from './PluginSurface'
import { ViewEmpty, ViewRenderer } from './ViewRenderer'
import './Plugins.css'

// /projects/:projectId/plugins/:pluginId/:pageId: a page a plugin contributes, in the project's tabs.
export function PluginPage() {
  const { pluginId = '', pageId = '' } = useParams<{ pluginId: string; pageId: string }>()
  const projectId = useStore(s => s.activeProjectId)
  const page = usePlugins(s => s.ui.pages.find(p => p.pluginId === pluginId && p.id === pageId))
  const loaded = usePlugins(s => s.loaded)
  const [attempt, setAttempt] = useState(0)
  const { tree, error, loading, setTree } = usePluginPageTree(pluginId, page, projectId, attempt, true)
  const onTree = useCallback((t: ViewNode) => setTree(t), [setTree])

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
          disabled={loading && !page.ui}
          title="Reload this page"
          aria-label="Reload this page"
        >
          <IconRefresh size={15} className={loading && !page.ui ? 'plugin-spin' : undefined} />
        </button>
      </header>}
      <div className="plugin-page__body">
        <PluginPageBody pluginId={pluginId} page={page} projectId={projectId} attempt={attempt} tree={tree} error={error} loading={loading} onTree={onTree} onRetry={() => setAttempt(a => a + 1)} />
      </div>
    </div>
  )
}

// A page a plugin puts in its own settings (placement "settings"): the same body, with a reload
// button instead of the project's title bar, and no project needed.
export function PluginSettingsPagePane({ pluginId, page }: { pluginId: string; page: PluginPageContribution }) {
  const projectId = useStore(s => s.activeProjectId)
  const [attempt, setAttempt] = useState(0)
  const { tree, error, loading, setTree } = usePluginPageTree(pluginId, page, projectId, attempt, false)
  const onTree = useCallback((t: ViewNode) => setTree(t), [setTree])
  return (
    <div className="plugin-page plugin-page--pane">
      <div className="plugin-logs__bar">
        <span className="plugin-logs__note">{page.label}</span>
        <button className="plugin-icon-btn" onClick={() => setAttempt(a => a + 1)} disabled={loading && !page.ui} title="Reload" aria-label="Reload">
          <IconRefresh size={14} className={loading && !page.ui ? 'plugin-spin' : undefined} />
        </button>
      </div>
      <div className="plugin-page__body">
        <PluginPageBody pluginId={pluginId} page={page} projectId={projectId} attempt={attempt} tree={tree} error={error} loading={loading} onTree={onTree} onRetry={() => setAttempt(a => a + 1)} />
      </div>
    </div>
  )
}

// Asks the plugin for the page's view tree, again when a webhook reaches it (what is already drawn
// stays until the answer). A project's page waits for the project; a page in settings does not.
function usePluginPageTree(pluginId: string, page: PluginPageContribution | undefined, projectId: string | null, attempt: number, needsProject: boolean) {
  // The answer is kept with the request it belongs to: a new request is "loading" until its own
  // answer comes, and the tree already drawn stays meanwhile.
  const [answer, setAnswer] = useState<{ key: string; tree: ViewNode | null; error: string } | null>(null)
  const webhookTick = usePlugins(s => s.webhookTick[pluginId] ?? 0)
  const render = page?.render
  const key = [pluginId, render, projectId, attempt, webhookTick].join('|')

  useEffect(() => {
    if (!render || (needsProject && !projectId)) return
    let cancelled = false
    api.callPluginRpc(pluginId, render, projectId, {})
      .then(result => {
        if (cancelled) return
        const outcome = classifyRpcResult(result)
        setAnswer({ key, tree: outcome.kind === 'tree' ? outcome.tree : null, error: '' })
        if (outcome.kind === 'toast') usePlugins.getState().toast(outcome.text, outcome.tone, pluginId)
      })
      .catch(e => { if (!cancelled) setAnswer(prev => ({ key, tree: prev?.tree ?? null, error: pluginErrorMessage(e) })) })
    return () => { cancelled = true }
  }, [pluginId, render, projectId, key, needsProject])

  const setTree = useCallback((tree: ViewNode | null) => setAnswer(prev => (prev ? { ...prev, tree } : prev)), [])
  return { tree: answer?.tree ?? null, error: answer?.key === key ? answer.error : '', loading: answer?.key !== key, setTree }
}

function PluginPageBody({ pluginId, page, projectId, attempt, tree, error, loading, onTree, onRetry }: {
  pluginId: string
  page: PluginPageContribution
  projectId: string | null
  attempt: number
  tree: ViewNode | null
  error: string
  loading: boolean
  onTree: (t: ViewNode) => void
  onRetry: () => void
}) {
  if (page.ui) return <PluginSurface key={attempt} pluginId={pluginId} pageId={page.id} projectId={projectId} />
  if (error) {
    return (
      <div className="plugin-page__failure" role="alert">
        <IconAlertTriangle size={22} stroke={1.6} aria-hidden />
        <p className="pv-empty__title">This page could not load</p>
        <p className="pv-empty__text">{error}</p>
        <button className="pv-btn pv-btn--secondary" onClick={onRetry}>Try again</button>
      </div>
    )
  }
  if (tree) return <ViewRenderer pluginId={pluginId} projectId={projectId} tree={tree} onTree={onTree} />
  if (loading) return <PageSkeleton />
  return <ViewEmpty title="Nothing to show" text="This plugin returned no content for this page." />
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
