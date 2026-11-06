import { Suspense, lazy, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { fromPromise } from 'xstate'
import { useMachine } from '@xstate/react'
import { IconPlayerPause, IconPlayerPlay, IconPlus, IconTrash, IconX } from '@tabler/icons-react'
import { useStore } from '../../store'
import { useIsMobile } from '../../hooks/useIsMobile'
import * as api from '../../api'
import type { Agent, Flow, FlowBlueprint, FlowGraph, FlowNodeField, FlowNodeType, FlowProblem, FlowRun, FlowStep, KanbanCard, KanbanList } from '../../types'
import type { FlowCanvasHandle } from './FlowCanvas'
import { editorMachine } from './editorMachine'
import { STEP_COLORS, nodeStatuses, parseFlowGraph, unknownNodeTypes } from './flowGraph'
import './Flows.css'

// litegraph is half a megabyte: it only loads for whoever opens this tab.
const FlowCanvas = lazy(() => import('./FlowCanvas').then(m => ({ default: m.FlowCanvas })))

type Selected = { id: string; type: string; config: Record<string, unknown> } | null

const CATEGORIES: [string, string][] = [['trigger', 'Triggers'], ['control', 'Control'], ['action', 'Actions']]

export function FlowsView() {
  const projectId = useStore(s => s.activeProjectId)
  const flowVersion = useStore(s => s.flowVersion)
  const flowDraftChange = useStore(s => s.flowDraftChange)
  const isMobile = useIsMobile()
  const [params, setParams] = useSearchParams()

  const [nodeTypes, setNodeTypes] = useState<FlowNodeType[]>([])
  const [flows, setFlows] = useState<Flow[]>([])
  const [flow, setFlow] = useState<Flow | null>(null)
  const [name, setName] = useState('')
  const [paused, setPaused] = useState(false)
  const [selected, setSelected] = useState<Selected>(null)
  const [tab, setTab] = useState<'node' | 'runs'>('node')
  const [runs, setRuns] = useState<FlowRun[]>([])
  const [viewed, setViewed] = useState<{ run: FlowRun; steps: FlowStep[] } | null>(null)
  const [agents, setAgents] = useState<Agent[]>([])
  const [lists, setLists] = useState<KanbanList[]>([])
  const [cards, setCards] = useState<KanbanCard[]>([])
  const [runCard, setRunCard] = useState('')
  const [notice, setNotice] = useState('')
  const [blueprints, setBlueprints] = useState<FlowBlueprint[]>([])

  const canvas = useRef<FlowCanvasHandle>(null)
  // The canvas loads late (and is rebuilt if remounted), so what it should show is kept here.
  const canvasLive = useRef(false)
  const showing = useRef<{ graph: FlowGraph; readOnly: boolean; statuses: Map<string, string>; keepView?: boolean } | null>(null)
  const flowRef = useRef<Flow | null>(null)
  const nameRef = useRef('')
  const nodeTypesRef = useRef(nodeTypes)
  flowRef.current = flow
  nameRef.current = name
  nodeTypesRef.current = nodeTypes

  const saveDraft = useCallback(async () => {
    const current = flowRef.current
    // an empty canvas that has not loaded yet must never overwrite a draft
    if (!current || !canvas.current || !canvasLive.current) return
    // nor one that could not draw every node: saving it would delete the ones it left out
    if (showing.current && unknownNodeTypes(showing.current.graph, nodeTypesRef.current).length > 0) return
    const saved = await api.saveFlow(current.id, { name: nameRef.current, draft: canvas.current.serialize() })
    setFlow(saved)
    setFlows(prev => prev.map(f => (f.id === saved.id ? saved : f)))
  }, [])

  const machine = useMemo(() => editorMachine.provide({
    actors: {
      save: fromPromise<void>(saveDraft),
      publish: fromPromise<FlowProblem[]>(async () => {
        const current = flowRef.current
        if (!current) return []
        const { problems } = await api.validateFlow(current.id)
        if (problems.length > 0) return problems
        const published = await api.publishFlow(current.id)
        setFlow(published)
        setFlows(prev => prev.map(f => (f.id === published.id ? published : f)))
        return []
      }),
    },
  }), [saveDraft])
  const [state, send] = useMachine(machine)
  const viewingRun = state.matches('viewingRun')
  const unknownTypes = useMemo(
    () => (flow && nodeTypes.length > 0 ? unknownNodeTypes(parseFlowGraph(flow.draft), nodeTypes) : []),
    [flow, nodeTypes],
  )
  const editable = !viewingRun && !isMobile && unknownTypes.length === 0

  const stateRef = useRef(state)
  stateRef.current = state

  // What is on the canvas reaches the database before the canvas shows anything else.
  const flush = useCallback(async () => {
    const s = stateRef.current
    if (s.matches('dirty') || s.matches('saving') || s.matches('saveFailed')) await saveDraft()
  }, [saveDraft])

  const reloadFlows = useCallback(async () => {
    if (!projectId) return
    setFlows(await api.listFlows(projectId))
  }, [projectId])

  useEffect(() => {
    api.listFlowNodeTypes().then(setNodeTypes).catch(e => setNotice(String(e)))
    api.listFlowBlueprints().then(setBlueprints).catch(console.error)
    return () => { flush().catch(console.error) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    if (!projectId) return
    reloadFlows().catch(e => setNotice(String(e)))
    api.getFlowsPaused(projectId).then(r => setPaused(r.paused)).catch(console.error)
    api.listAgents(projectId).then(setAgents).catch(console.error)
    api.listKanbanLists(projectId).then(setLists).catch(console.error)
    api.listKanbanCards(projectId).then(setCards).catch(console.error)
  }, [projectId, reloadFlows])

  const paint = useCallback(() => {
    const view = showing.current
    if (!canvasLive.current || !view) return
    canvas.current?.load(view.graph, view.readOnly || unknownNodeTypes(view.graph, nodeTypesRef.current).length > 0, view.keepView)
    canvas.current?.setStatuses(view.statuses)
  }, [])

  const onCanvasReady = useCallback((ready: boolean) => {
    canvasLive.current = ready
    if (ready) paint()
  }, [paint])

  const show = useCallback((graph: FlowGraph, readOnly: boolean, statuses = new Map<string, string>()) => {
    showing.current = { graph, readOnly, statuses }
    paint()
  }, [paint])

  const openFlow = useCallback(async (f: Flow) => {
    await flush()
    const fresh = await api.getFlow(f.id)
    setFlow(fresh)
    setName(fresh.name)
    setViewed(null)
    setSelected(null)
    show(parseFlowGraph(fresh.draft), isMobile)
    canvas.current?.setProblems([])
    send({ type: 'OPEN' })
  }, [flush, isMobile, send, show])

  const showRun = useCallback(async (id: string) => {
    await flush()
    const detail = await api.getFlowRun(id)
    show(parseFlowGraph(detail.graph), true, nodeStatuses(detail.steps))
    setViewed({ run: detail.run, steps: detail.steps })
    setSelected(null)
    send({ type: 'VIEW_RUN' })
  }, [flush, send, show])

  const backToDraft = useCallback(() => {
    if (flow) openFlow(flow).catch(e => setNotice(String(e)))
    setParams({}, { replace: true })
  }, [flow, openFlow, setParams])

  // The Running tab links here with ?flow=&run=.
  const canvasReady = nodeTypes.length > 0
  const linked = useRef('')
  useEffect(() => {
    const flowId = params.get('flow')
    const runId = params.get('run')
    if (!canvasReady || !flowId || !flows.length) return
    const key = `${flowId}/${runId}`
    if (linked.current === key) return
    const f = flows.find(x => x.id === flowId)
    if (!f) return
    linked.current = key
    openFlow(f).then(() => {
      if (runId) { setTab('runs'); return showRun(runId) }
    }).catch(e => setNotice(String(e)))
  }, [params, flows, canvasReady, openFlow, showRun])

  // Live: runs and the run on screen follow the hub's events.
  const flowId = flow?.id
  const viewedId = viewed?.run.id
  useEffect(() => {
    if (!flowId) { setRuns([]); return }
    let stale = false
    const t = setTimeout(() => {
      api.listFlowRuns(flowId).then(r => { if (!stale) setRuns(r) }).catch(console.error)
      if (viewedId) {
        api.getFlowRun(viewedId).then(d => {
          if (stale) return
          setViewed({ run: d.run, steps: d.steps })
          const statuses = nodeStatuses(d.steps)
          if (showing.current) showing.current.statuses = statuses
          canvas.current?.setStatuses(statuses)
        }).catch(console.error)
      }
    }, 200) // a run changes in bursts
    return () => { stale = true; clearTimeout(t) }
  }, [flowId, viewedId, flowVersion, tab]) // opening Runs refetches too, in case the socket missed an event

  // Live: an agent changed a draft. The last save wins, so what it wrote replaces what is on the
  // canvas, unsaved edits included. A run on screen stays: going back to the draft reads it again.
  useEffect(() => {
    if (flowDraftChange.version === 0) return
    let stale = false
    reloadFlows().catch(console.error)
    if (flowRef.current?.id !== flowDraftChange.flow_id) return
    api.getFlow(flowDraftChange.flow_id).then(fresh => {
      if (stale || flowRef.current?.id !== fresh.id) return
      setFlow(fresh)
      if (stateRef.current.matches('viewingRun')) return
      setName(fresh.name)
      showing.current = { graph: parseFlowGraph(fresh.draft), readOnly: isMobile, statuses: new Map(), keepView: true }
      paint()
      canvas.current?.setProblems([])
      send({ type: 'OPEN' })
    }).catch(e => setNotice(String(e)))
    return () => { stale = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [flowDraftChange.version])

  useEffect(() => {
    canvas.current?.setProblems(state.context.problems.flatMap(p => (p.node_id ? [p.node_id] : [])))
  }, [state.context.problems])

  const typeOf = useCallback((t: string) => nodeTypes.find(n => n.type === t), [nodeTypes])

  async function createFlow() {
    if (!projectId) return
    const n = window.prompt('Name of the flow')
    if (!n?.trim()) return
    try {
      const f = await api.createFlow(projectId, n.trim())
      await reloadFlows()
      await openFlow(f)
    } catch (e) { setNotice(String(e)) }
  }

  // A copy starts as a draft: it does nothing until someone reads it and publishes it.
  async function createFromBlueprint(b: FlowBlueprint) {
    if (!projectId) return
    try {
      const f = await api.createFlow(projectId, b.name, b.key)
      await reloadFlows()
      await openFlow(f)
    } catch (e) { setNotice(String(e)) }
  }

  async function removeFlow() {
    if (!flow || !window.confirm(`Delete the flow "${flow.name}" and its run history?`)) return
    try {
      await api.deleteFlow(flow.id)
      setFlow(null)
      show({ nodes: [], edges: [] }, true)
      send({ type: 'CLOSE' })
      await reloadFlows()
    } catch (e) { setNotice(String(e)) }
  }

  async function toggleEnabled() {
    if (!flow) return
    try {
      const f = await api.setFlowEnabled(flow.id, !flow.enabled)
      setFlow(f)
      setFlows(prev => prev.map(x => (x.id === f.id ? f : x)))
    } catch (e) { setNotice(String(e)) }
  }

  async function togglePaused() {
    if (!projectId) return
    try {
      const r = await api.setFlowsPaused(projectId, !paused)
      setPaused(r.paused)
    } catch (e) { setNotice(String(e)) }
  }

  async function runNow() {
    if (!flow) return
    try {
      const run = await api.runFlow(flow.id, runCard || undefined)
      setTab('runs')
      await showRun(run.id)
    } catch (e) { setNotice(String(e)) }
  }

  function updateConfig(field: string, value: unknown) {
    if (!selected) return
    const config = { ...selected.config, [field]: value }
    if (value === '') delete config[field]
    setSelected({ ...selected, config })
    canvas.current?.setConfig(selected.id, config)
  }

  const saveLabel =
    state.matches('saving') ? 'Saving…'
    : state.matches('publishing') ? 'Publishing…'
    : state.matches('dirty') ? 'Unsaved'
    : state.matches('saveFailed') ? 'Not saved'
    : flow?.draft_agent ? `Changed by ${flow.draft_agent}: review it before publishing`
    : state.context.unpublished ? 'Saved, not published'
    : 'Saved'

  const problems = state.context.problems
  const selectedType = selected ? typeOf(selected.type) : undefined
  const hasManual = useMemo(() => flow && parseFlowGraph(flow.draft).nodes.some(n => n.type === 'trigger.manual'), [flow])

  return (
    <div className="flows">
      <aside className="flows-side">
        <div className="flows-side__head">
          <span>Flows</span>
          <button className="header-icon-btn" onClick={createFlow} title="New flow" aria-label="New flow"><IconPlus size={15} /></button>
        </div>
        <div className="flows-list">
          {flows.map(f => (
            <button
              key={f.id}
              className={`flows-list__item${flow?.id === f.id ? ' flows-list__item--active' : ''}`}
              onClick={() => { setParams({}, { replace: true }); openFlow(f).catch(e => setNotice(String(e))) }}
            >
              <span className={`flows-dot${f.enabled && f.published_version > 0 ? ' flows-dot--on' : ''}`} />
              <span className="flows-list__name">{f.name}</span>
              {f.published_version > 0 && <span className="flows-list__version">v{f.published_version}</span>}
            </button>
          ))}
          {flows.length === 0 && <p className="flows-hint">No flows yet. A flow reacts to something on the board and acts on it.</p>}
        </div>
        <button
          className={`flows-pause${paused ? ' flows-pause--on' : ''}`}
          onClick={togglePaused}
          title="Stops new runs and holds running ones for the whole project"
        >
          {paused ? <IconPlayerPlay size={14} /> : <IconPlayerPause size={14} />}
          {paused ? 'All flows paused: resume' : 'Pause all flows'}
        </button>
        {editable && blueprints.some(b => !b.seeded) && (
          <div className="flows-blueprints">
            <div className="flows-palette__title">Start from a blueprint</div>
            {blueprints.filter(b => !b.seeded).map(b => (
              <button key={b.key} className="flows-blueprints__item" title={b.description} onClick={() => createFromBlueprint(b)}>{b.name}</button>
            ))}
          </div>
        )}
        {editable && flow && (
          <div className="flows-palette">
            {CATEGORIES.map(([cat, label]) => (
              <div key={cat}>
                <div className="flows-palette__title">{label}</div>
                {nodeTypes.filter(t => t.category === cat).map(t => (
                  <button
                    key={t.type}
                    className={`flows-palette__item flows-palette__item--${cat}`}
                    title={t.description}
                    onClick={() => canvas.current?.addNode(t.type)}
                  >{t.title}</button>
                ))}
              </div>
            ))}
          </div>
        )}
      </aside>

      <section className="flows-main">
        <div className="flows-bar">
          {flow ? (
            <>
              {viewed ? (
                <>
                  <button className="btn btn--secondary" onClick={backToDraft}>Back to draft</button>
                  <span className="flows-bar__label">
                    Run on v{viewed.run.version}: <RunStatus status={viewed.run.status} />
                  </span>
                  {['running', 'queued'].includes(viewed.run.status) && (
                    <button className="btn btn--secondary" onClick={() => api.cancelFlowRun(viewed.run.id).catch(e => setNotice(String(e)))}>Cancel run</button>
                  )}
                </>
              ) : (
                <>
                  <input
                    className="input flows-bar__name"
                    aria-label="Flow name"
                    value={name}
                    disabled={!editable}
                    onChange={e => { setName(e.target.value); send({ type: 'CHANGE' }) }}
                  />
                  <span className="flows-bar__label" role="status">{saveLabel}</span>
                  <span className="flows-bar__gap" />
                  {hasManual && flow.published_version > 0 && (
                    <>
                      <select className="input flows-bar__card" aria-label="Card for the run" value={runCard} onChange={e => setRunCard(e.target.value)}>
                        <option value="">No card</option>
                        {cards.map(c => <option key={c.id} value={c.id}>{c.title}</option>)}
                      </select>
                      <button className="btn btn--secondary" onClick={runNow}>Run</button>
                    </>
                  )}
                  {flow.published_version > 0 && (
                    <button className="btn btn--secondary" onClick={toggleEnabled}>{flow.enabled ? 'Disable' : 'Enable'}</button>
                  )}
                  {editable && (
                    <button className="btn btn--primary" onClick={() => send({ type: 'PUBLISH' })} disabled={state.matches('publishing')}>Publish</button>
                  )}
                  {editable && (
                    <button className="header-icon-btn" onClick={removeFlow} title="Delete flow" aria-label="Delete flow"><IconTrash size={15} /></button>
                  )}
                </>
              )}
            </>
          ) : <span className="flows-bar__label">Choose a flow or create one</span>}
        </div>
        {(notice || state.context.error) && (
          <div className="flows-notice" role="alert">
            <span>{notice || state.context.error}</span>
            {notice && <button className="header-icon-btn" onClick={() => setNotice('')} aria-label="Dismiss"><IconX size={14} /></button>}
          </div>
        )}
        {isMobile && flow && <div className="flows-notice">Flows can be viewed here. Edit them on a larger screen.</div>}
        {unknownTypes.length > 0 && !viewed && (
          <div className="flows-notice" role="alert">
            This flow uses nodes the running hub does not know ({unknownTypes.join(', ')}), so they are not drawn and nothing here is saved. Restart the hub, then reload this page.
          </div>
        )}
        {canvasReady && (
          <Suspense fallback={<div className="flows-hint">Loading editor…</div>}>
            <FlowCanvas
              ref={canvas}
              nodeTypes={nodeTypes}
              onChange={() => send({ type: 'CHANGE' })}
              onSelect={n => { setSelected(n); if (n) setTab('node') }}
              onReady={onCanvasReady}
            />
          </Suspense>
        )}
      </section>

      {flow && (
        <aside className="flows-panel">
          <div className="flows-tabs" role="tablist">
            <button role="tab" aria-selected={tab === 'node'} className={`flows-tab${tab === 'node' ? ' flows-tab--active' : ''}`} onClick={() => setTab('node')}>
              Node{problems.length > 0 && <span className="flows-tab__count">{problems.length}</span>}
            </button>
            <button role="tab" aria-selected={tab === 'runs'} className={`flows-tab${tab === 'runs' ? ' flows-tab--active' : ''}`} onClick={() => setTab('runs')}>Runs</button>
          </div>
          {tab === 'node' ? (
            <div className="flows-panel__body">
              {problems.length > 0 && (
                <div className="flows-problems">
                  <strong>Fix these to publish</strong>
                  <ul>{problems.map((p, i) => <li key={i}>{p.message}</li>)}</ul>
                </div>
              )}
              {selected && selectedType ? (
                <NodeForm
                  type={selectedType}
                  config={selected.config}
                  disabled={!editable}
                  agents={agents}
                  lists={lists}
                  nodeId={selected.id}
                  step={viewed?.steps.filter(s => s.node_id === selected.id).at(-1)}
                  onDecided={() => { if (viewed) showRun(viewed.run.id).catch(e => setNotice(String(e))) }}
                  onChange={updateConfig}
                />
              ) : (
                <p className="flows-hint">
                  {viewed ? 'Select a node to see what it did.' : 'Add a trigger, connect the nodes by dragging from an output, and select one to set it up. Publish to make it active.'}
                </p>
              )}
            </div>
          ) : (
            <div className="flows-panel__body">
              {runs.length === 0 && <p className="flows-hint">No runs yet.</p>}
              {runs.map(r => (
                <button
                  key={r.id}
                  className={`flows-run${viewed?.run.id === r.id ? ' flows-run--active' : ''}`}
                  onClick={() => showRun(r.id).catch(e => setNotice(String(e)))}
                >
                  <RunStatus status={r.status} />
                  <span className="flows-run__time">{new Date(r.started_at).toLocaleString()}</span>
                  <span className="flows-run__card">{cards.find(c => c.id === r.card_id)?.title ?? (r.card_id ? 'Deleted card' : 'No card')} · v{r.version}</span>
                  {r.error && <span className="flows-run__error">{r.error}</span>}
                </button>
              ))}
            </div>
          )}
        </aside>
      )}
    </div>
  )
}

function RunStatus({ status }: { status: string }) {
  return <span className="flows-status" style={{ color: STEP_COLORS[status] ?? 'var(--text-muted)' }}>{status}</span>
}

// The key goes to the hub and never comes back: this only knows whether there is one.
function AIKeyField() {
  const [state, setState] = useState<{ configured: boolean; source: string } | null>(null)
  const [key, setKey] = useState('')
  const [error, setError] = useState('')
  useEffect(() => { api.getFlowAISettings().then(setState).catch(e => setError(String(e))) }, [])

  async function save(call: Promise<{ configured: boolean; source: string }>) {
    try {
      setState(await call)
      setKey('')
      setError('')
    } catch (e) { setError(e instanceof Error ? e.message : String(e)) }
  }

  return (
    <div className="flows-key">
      <span className="flows-key__title">Anthropic API key</span>
      {state?.configured ? (
        <span className="flows-key__state">
          {state.source === 'environment' ? 'Using ANTHROPIC_API_KEY from the hub.' : 'Saved on the hub.'}
        </span>
      ) : (
        <span className="flows-key__state flows-key__state--missing">Without a key this node fails.</span>
      )}
      <div className="flows-key__row">
        <input
          className="input" type="password" aria-label="Anthropic API key" autoComplete="off"
          placeholder={state?.configured ? 'Replace the key' : 'sk-ant-…'}
          value={key} onChange={e => setKey(e.target.value)}
        />
        <button className="btn btn--secondary" disabled={!key.trim()} onClick={() => save(api.saveFlowAIKey(key.trim()))}>Save</button>
        {state?.source === 'saved' && <button className="btn btn--secondary" onClick={() => save(api.clearFlowAIKey())}>Remove</button>}
      </div>
      {error && <p className="flows-run__error">{error}</p>}
    </div>
  )
}

// A question in a run, answered right here (the bell is the other place).
function DecisionForm({ seq, question, onDecided }: { seq: number; question: string; onDecided(): void }) {
  const [note, setNote] = useState('')
  const [error, setError] = useState('')
  async function decide(decision: 'approved' | 'rejected') {
    try {
      await api.decideFlowApproval(seq, decision, note)
      onDecided()
    } catch (e) { setError(e instanceof Error ? e.message : String(e)) }
  }
  return (
    <div className="flows-decision">
      <p className="flows-decision__question">{question}</p>
      <input className="input" aria-label="Note for the decision" placeholder="Note (optional)" value={note} onChange={e => setNote(e.target.value)} />
      <div className="flows-decision__row">
        <button className="btn btn--approve" onClick={() => decide('approved')}>Approve</button>
        <button className="btn btn--deny" onClick={() => decide('rejected')}>Reject</button>
      </div>
      {error && <p className="flows-run__error">{error}</p>}
    </div>
  )
}

function NodeForm({ type, config, disabled, agents, lists, nodeId, step, onChange, onDecided }: {
  type: FlowNodeType
  config: Record<string, unknown>
  disabled: boolean
  agents: Agent[]
  lists: KanbanList[]
  nodeId: string
  step?: FlowStep
  onChange(field: string, value: unknown): void
  onDecided(): void
}) {
  const deciding = type.type === 'control.human_approval' && step?.status === 'waiting'
  return (
    <div className="flows-form" key={nodeId}>
      <h3 className="flows-form__title">{type.title}</h3>
      <p className="flows-form__desc">{type.description}</p>
      {(type.fields ?? []).map(f => (
        <label key={f.name} className={`flows-field${f.kind === 'bool' ? ' flows-field--check' : ''}`}>
          {f.kind === 'bool' ? (
            <span className="flows-field__row">
              <input type="checkbox" checked={config[f.name] === undefined ? f.default === true : config[f.name] === true} disabled={disabled} onChange={e => onChange(f.name, e.target.checked)} />
              {f.label}
            </span>
          ) : (
            <>
              <span>{f.label}{f.required ? ' *' : ''}</span>
              <FieldInput field={f} value={config[f.name]} disabled={disabled} agents={agents} lists={lists} onChange={v => onChange(f.name, v)} />
            </>
          )}
          {f.help && <small>{f.help}</small>}
          {(f.kind === 'expr' || f.kind === 'template') && !f.help && <small>{f.kind === 'template' ? 'Insert values with {{card.title}}.' : 'Reads card, event, run and nodes.'}</small>}
        </label>
      ))}
      {type.type === 'action.ai_evaluate' && !disabled && <AIKeyField />}
      <p className="flows-form__id">Other nodes read its output as <code>nodes.{nodeId}</code></p>
      {step && (
        <div className="flows-step">
          <RunStatus status={step.status} />
          {step.port && step.port !== '*' && <span> through “{step.port}”</span>}
          {step.error && <p className="flows-run__error">{step.error}</p>}
          {deciding ? (
            <DecisionForm seq={step.seq} question={String((step.output as { question?: unknown })?.question ?? '')} onDecided={onDecided} />
          ) : step.output && step.output !== '{}' && <pre>{JSON.stringify(step.output, null, 2)}</pre>}
        </div>
      )}
    </div>
  )
}

function FieldInput({ field, value, disabled, agents, lists, onChange }: {
  field: FlowNodeField
  value: unknown
  disabled: boolean
  agents: Agent[]
  lists: KanbanList[]
  onChange(v: unknown): void
}) {
  const text = value === undefined || value === null ? '' : String(value)
  switch (field.kind) {
    case 'select':
      return (
        <select className="input" value={text} disabled={disabled} onChange={e => onChange(e.target.value)}>
          {!field.required && !(field.options ?? []).includes('any') && <option value="">—</option>}
          {(field.options ?? []).map(o => <option key={o} value={o}>{o}</option>)}
        </select>
      )
    case 'agent':
      return (
        <select className="input" value={text} disabled={disabled} onChange={e => onChange(e.target.value)}>
          <option value="">—</option>
          {agents.map(a => <option key={a.id} value={a.name}>{a.name}</option>)}
        </select>
      )
    case 'list':
      return (
        <select className="input" value={text} disabled={disabled} onChange={e => onChange(e.target.value)}>
          <option value="">—</option>
          {lists.map(l => <option key={l.id} value={l.id}>{l.name}</option>)}
        </select>
      )
    case 'number':
      return (
        <input
          className="input" type="number" min={field.min} value={text} disabled={disabled}
          onChange={e => onChange(e.target.value === '' ? '' : Number(e.target.value))}
        />
      )
    case 'template':
      return <textarea className="input flows-field__text" rows={3} value={text} disabled={disabled} onChange={e => onChange(e.target.value)} />
    case 'lines':
      return <textarea className="input flows-field__text flows-field__code" rows={3} spellCheck={false} value={text} disabled={disabled} onChange={e => onChange(e.target.value)} />
    default:
      return (
        <input
          className={`input${field.kind === 'expr' ? ' flows-field__code' : ''}`} value={text} disabled={disabled}
          spellCheck={field.kind !== 'expr'} onChange={e => onChange(e.target.value)}
        />
      )
  }
}
