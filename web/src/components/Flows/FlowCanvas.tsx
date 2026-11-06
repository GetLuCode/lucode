import { forwardRef, useEffect, useImperativeHandle, useRef } from 'react'
import { LGraph, LGraphCanvas, LiteGraph } from 'litegraph.js'
import 'litegraph.js/css/litegraph.css'
import type { FlowGraph, FlowNodeType } from '../../types'
import { STEP_COLORS, canvasType, defaultConfig, nodeSummary, portLabels, serializeCanvas, type CanvasLink, type CanvasNode } from './flowGraph'

// The litegraph canvas. It only draws and edits: the hub validates and runs flows, and
// litegraph's own graph runtime is never started.

/* eslint-disable @typescript-eslint/no-explicit-any */
const LG = LiteGraph as any

const SLOT = 'flow'
const NODE_WIDTH = 210
const SUMMARY_HEIGHT = 26
const CATEGORY_COLOR: Record<string, [string, string]> = {
  trigger: ['#7a4f9e', '#231a2c'],
  control: ['#3f6f9e', '#18222c'],
  action: ['#2f7a68', '#15251f'],
}

export interface FlowCanvasHandle {
  load(graph: FlowGraph, readOnly: boolean, keepView?: boolean): void
  serialize(): FlowGraph
  addNode(type: string): void
  setConfig(flowId: string, config: Record<string, unknown>): void
  setStatuses(statuses: Map<string, string>): void
  setProblems(nodeIds: string[]): void
}

interface Props {
  nodeTypes: FlowNodeType[]
  onChange(): void
  onSelect(node: { id: string; type: string; config: Record<string, unknown> } | null): void
  // The handle only works from here on. A new canvas is empty: whoever owns it loads it.
  onReady(ready: boolean): void
}

let registered = false

// The name on an output is only a label: the port keeps its id, which is what edges store.
function labelPorts(node: any, t: FlowNodeType, config: Record<string, unknown>) {
  const labels = portLabels(t, config)
  for (const out of node.outputs ?? []) out.label = labels.get(out.name)
}

// Replaces litegraph's stock nodes, some of which run typed code with new Function, with
// the hub's list. Only these types can exist on the canvas.
function registerTypes(types: FlowNodeType[]) {
  if (registered) return
  registered = true
  LG.clearRegisteredTypes()
  LG.pointerevents_method = 'pointer' // touch works as well as the mouse
  LG.NODE_TITLE_COLOR = '#e8e4dd'
  LG.NODE_SELECTED_TITLE_COLOR = '#ffffff'
  LG.NODE_TEXT_COLOR = '#b9b6bd'
  for (const t of types) {
    function Node(this: any) {
      if (t.category !== 'trigger') this.addInput('', SLOT)
      for (const port of t.ports ?? []) this.addOutput(port, SLOT)
      this.properties = { flowId: undefined, config: defaultConfig(t) }
      labelPorts(this, t, this.properties.config)
      this.resizable = false
      this.size = this.computeSize()
      const [title, body] = CATEGORY_COLOR[t.category] ?? CATEGORY_COLOR.action
      this.color = title
      this.bgcolor = body
      this.lucodeColor = title
    }
    const proto = Node.prototype as any
    // litegraph calls this whenever a slot changes: the width stays, and the line under the
    // slots keeps its own room however many inputs the node has grown.
    proto.computeSize = function (this: any) {
      const slots = Math.max(this.inputs?.length ?? 0, this.outputs?.length ?? 0, 1)
      return [NODE_WIDTH, slots * LG.NODE_SLOT_HEIGHT + SUMMARY_HEIGHT]
    }
    // litegraph takes one link per input, so an input is added whenever all are in use:
    // several paths can arrive at any node, which Join and merging branches need.
    proto.onConnectionsChange = function (this: any, kind: number) {
      if (kind !== LG.INPUT || !this.inputs) return
      const free = this.inputs.filter((i: any) => i.link == null).length
      if (free === 0) {
        this.addInput('', SLOT)
      } else if (free > 1) {
        for (let i = this.inputs.length - 1; i > 0; i--) {
          if (this.inputs[i].link == null) { this.removeInput(i); break }
        }
      }
    }
    // Only unlinking: the stock menu can rename or remove a slot, which would break a port.
    proto.getSlotMenuOptions = function (slot: any) {
      const linked = slot.output?.links?.length || slot.input?.link != null
      return linked ? [{ content: 'Disconnect', slot }] : []
    }
    proto.onDrawForeground = function (this: any, ctx: CanvasRenderingContext2D) {
      if (this.flags?.collapsed) return
      ctx.save()
      // litegraph's own has_errors floods the node with pure red: an outline says enough
      if (this.lucodeProblem) {
        ctx.strokeStyle = '#d9534f'
        ctx.lineWidth = 2
        ctx.strokeRect(-1, -LG.NODE_TITLE_HEIGHT - 1, this.size[0] + 2, this.size[1] + LG.NODE_TITLE_HEIGHT + 2)
      }
      const text = nodeSummary(t, this.properties.config ?? {})
      if (text) {
        ctx.font = '11px sans-serif'
        ctx.fillStyle = '#9a97a0'
        ctx.textAlign = 'left'
        ctx.fillText(text, 8, this.size[1] - 8, this.size[0] - 16)
      }
      ctx.restore()
    }
    ;(Node as any).title = t.title
    LG.registerNodeType(canvasType(t), Node)
  }
}

export const FlowCanvas = forwardRef<FlowCanvasHandle, Props>(function FlowCanvas({ nodeTypes, onChange, onSelect, onReady }, ref) {
  const box = useRef<HTMLDivElement>(null)
  const el = useRef<HTMLCanvasElement>(null)
  const graph = useRef<any>(null)
  const canvas = useRef<any>(null)
  const loading = useRef(false)
  const typeOf = useRef(new Map<string, FlowNodeType>())
  const events = useRef({ onChange, onSelect, onReady })
  events.current = { onChange, onSelect, onReady }

  useEffect(() => {
    registerTypes(nodeTypes)
    typeOf.current = new Map(nodeTypes.map(t => [t.type, t]))
    const g: any = new LGraph()
    const c: any = new LGraphCanvas(el.current as any, g)
    graph.current = g
    canvas.current = c

    const css = getComputedStyle(document.documentElement)
    c.clear_background_color = css.getPropertyValue('--bg').trim() || '#0c0c0e'
    c.allow_searchbox = false
    c.render_canvas_border = false
    c.show_info = false
    c.connections_width = 2

    const changed = () => { if (!loading.current) events.current.onChange() }
    g.onNodeAdded = (n: any) => {
      if (loading.current) return
      n.properties.flowId = `n${n.id}` // also gives a pasted copy its own identity
      changed()
    }
    g.onNodeRemoved = changed
    g.onNodeConnectionChange = changed
    c.onNodeMoved = changed

    const select = (n: any) => {
      if (!n) return events.current.onSelect(null)
      events.current.onSelect({ id: n.properties.flowId, type: n.type.slice(n.type.indexOf('/') + 1), config: n.properties.config ?? {} })
    }
    // also fires when a node is selected from code, which onNodeSelected does not
    c.onSelectionChange = (nodes: Record<string, any>) => {
      const list = Object.values(nodes ?? {})
      select(list.length === 1 ? list[0] : null)
    }
    c.onShowNodePanel = select // the form on the right, not litegraph's own panel

    // Short menus: the stock ones offer modes, shapes and a property editor that evals input.
    c.getCanvasMenuOptions = () => c.read_only ? [] : [{ content: 'Add node', has_submenu: true, callback: (LGraphCanvas as any).onMenuAdd }]
    c.getNodeMenuOptions = (n: any) => c.read_only ? [] : [
      { content: 'Duplicate', callback: () => { const copy = n.clone(); if (copy) { copy.pos = [n.pos[0] + 30, n.pos[1] + 30]; g.add(copy) } } },
      { content: 'Delete', callback: () => { g.remove(n); select(null) } },
    ]

    // litegraph's keyboard shortcuts ignore read_only: a past run must not change on screen
    for (const name of ['deleteSelectedNodes', 'pasteFromClipboard']) {
      const original = c[name]
      c[name] = function (this: any, ...args: unknown[]) { if (!c.read_only) return original.apply(c, args) }
    }

    const resize = () => {
      const r = box.current?.getBoundingClientRect()
      if (r && r.width > 0 && r.height > 0) c.resize(Math.floor(r.width), Math.floor(r.height))
    }
    // read_only makes litegraph ignore clicks on nodes, but a past run is read by selecting them
    const inspect = (e: PointerEvent) => {
      if (!c.read_only || e.button !== 0) return
      const r = el.current!.getBoundingClientRect()
      const [x, y] = c.ds.convertCanvasToOffset([e.clientX - r.left, e.clientY - r.top])
      const node = g.getNodeOnPos(x, y, c.visible_nodes, 5)
      if (node) c.selectNode(node)
      else c.deselectAllNodes()
      c.setDirty(true, true)
    }
    el.current!.addEventListener('pointerdown', inspect, true)

    const observer = new ResizeObserver(resize)
    if (box.current) observer.observe(box.current)
    resize()
    events.current.onReady(true)

    return () => {
      events.current.onReady(false)
      el.current?.removeEventListener('pointerdown', inspect, true)
      observer.disconnect()
      c.stopRendering()
      c.unbindEvents()
      c.setGraph(null)
      graph.current = null
      canvas.current = null
    }
    // the canvas lives as long as the view; the types come once from the hub
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const byFlowId = (id: string) => (graph.current?._nodes ?? []).find((n: any) => n.properties.flowId === id)

  useImperativeHandle(ref, () => ({
    load(data, readOnly, keepView) {
      const g = graph.current
      const c = canvas.current
      if (!g || !c) return
      // a live reload leaves the view where the person has it, unless there was nothing to see
      const holdView = keepView && (g._nodes?.length ?? 0) > 0
      loading.current = true
      try {
        g.clear()
        const made = new Map<string, any>()
        for (const n of data.nodes) {
          const def = typeOf.current.get(n.type)
          if (!def) continue // a type this build does not know: publishing will report it
          const node = LG.createNode(canvasType(def))
          const num = /^n(\d+)$/.exec(n.id)
          if (num) node.id = Number(num[1])
          node.pos = [n.x, n.y]
          node.properties.flowId = n.id
          node.properties.config = { ...(n.config ?? {}) }
          labelPorts(node, def, node.properties.config)
          g.add(node)
          made.set(n.id, node)
        }
        for (const e of data.edges) {
          const from = made.get(e.from)
          const to = made.get(e.to)
          if (!from || !to) continue
          const out = from.findOutputSlot(e.port)
          if (out < 0 || !to.inputs) continue
          const free = to.inputs.findIndex((i: any) => i.link == null)
          from.connect(out, to, free < 0 ? to.inputs.length - 1 : free)
        }
      } finally {
        loading.current = false
      }
      c.read_only = readOnly
      c.allow_reconnect_links = !readOnly
      c.deselectAllNodes()
      if (!holdView) c.ds.reset()
      if (!holdView && data.nodes.length) {
        // the whole flow fits on opening: nothing is hidden past the edge
        const minX = Math.min(...data.nodes.map(n => n.x))
        const minY = Math.min(...data.nodes.map(n => n.y))
        const maxX = Math.max(...data.nodes.map(n => n.x)) + NODE_WIDTH
        const maxY = Math.max(...data.nodes.map(n => n.y)) + 160
        const scale = Math.min(1, ((c.canvas.width as number) - 80) / (maxX - minX), ((c.canvas.height as number) - 80) / (maxY - minY))
        c.ds.scale = Math.max(0.3, scale)
        c.ds.offset = [40 / c.ds.scale - minX, 40 / c.ds.scale - minY]
      }
      c.setDirty(true, true)
      events.current.onSelect(null)
    },
    serialize() {
      const g = graph.current
      if (!g) return { nodes: [], edges: [] }
      return serializeCanvas(g._nodes as CanvasNode[], g.links as Record<string, CanvasLink>)
    },
    addNode(type) {
      const g = graph.current
      const c = canvas.current
      const def = typeOf.current.get(type)
      if (!g || !c || !def || c.read_only) return
      const node = LG.createNode(canvasType(def))
      const [cx, cy] = c.ds.convertCanvasToOffset([(c.canvas.width as number) / 2, (c.canvas.height as number) / 2])
      const count = g._nodes.length
      node.pos = [cx - NODE_WIDTH / 2 + (count % 5) * 24, cy - 40 + (count % 5) * 24]
      g.add(node)
      c.selectNode(node)
      c.setDirty(true, true)
    },
    setConfig(flowId, config) {
      const n = byFlowId(flowId)
      if (!n) return
      n.properties.config = config
      const def = typeOf.current.get(n.type.slice(n.type.indexOf('/') + 1))
      if (def) labelPorts(n, def, config)
      canvas.current?.setDirty(true, true)
      events.current.onChange()
    },
    setStatuses(statuses) {
      for (const n of graph.current?._nodes ?? []) {
        const s = statuses.get(n.properties.flowId)
        n.color = s ? STEP_COLORS[s] : n.lucodeColor
        n.boxcolor = s ? STEP_COLORS[s] : undefined
      }
      canvas.current?.setDirty(true, true)
    },
    setProblems(ids) {
      const bad = new Set(ids)
      for (const n of graph.current?._nodes ?? []) n.lucodeProblem = bad.has(n.properties.flowId)
      canvas.current?.setDirty(true, true)
    },
  }), [])

  return (
    <div className="flows-canvas" ref={box}>
      <canvas ref={el} />
    </div>
  )
})
