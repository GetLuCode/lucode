import type { FlowEdge, FlowGraph, FlowNode, FlowNodeType, FlowPin } from '../../types'

// The translation between the canvas and lucode's stored graph. It has no litegraph import
// so it can be tested without a browser. The stored graph belongs to lucode: if the
// editor ever changes, saved flows stay as they are.

// What serializing needs from a litegraph node and link.
export interface CanvasNode {
  id: number
  type: string
  pos: ArrayLike<number>
  properties: { flowId?: string; config?: Record<string, unknown> }
  outputs?: { name: string; links?: number[] | null }[] | null
}
export interface CanvasLink { target_id: number }

// litegraph files a node under "<category>/<type>", which is also what groups its menu.
export const canvasType = (t: Pick<FlowNodeType, 'category' | 'type'>) => `${t.category}/${t.type}`
export const flowType = (canvas: string) => canvas.slice(canvas.indexOf('/') + 1)

export function serializeCanvas(nodes: CanvasNode[], links: Record<string, CanvasLink | undefined>): FlowGraph {
  const flowId = new Map<number, string>()
  for (const n of nodes) flowId.set(n.id, n.properties.flowId ?? `n${n.id}`)
  const outNodes: FlowNode[] = nodes.map(n => ({
    id: flowId.get(n.id)!,
    type: flowType(n.type),
    x: Math.round(n.pos[0]),
    y: Math.round(n.pos[1]),
    config: n.properties.config ?? {},
  }))
  const edges: FlowEdge[] = []
  for (const n of nodes) {
    for (const out of n.outputs ?? []) {
      for (const linkId of out.links ?? []) {
        const to = flowId.get(links[linkId]?.target_id ?? -1)
        if (to) edges.push({ from: flowId.get(n.id)!, port: out.name, to })
      }
    }
  }
  return { nodes: outNodes, edges }
}

export function parseFlowGraph(raw: unknown): FlowGraph {
  const g = (typeof raw === 'string' ? JSON.parse(raw) : raw) as Partial<FlowGraph> | null
  return { nodes: g?.nodes ?? [], edges: g?.edges ?? [] }
}

// Node types in a graph that this hub does not offer, which the canvas cannot draw. Such a
// graph must never be saved from the canvas: those nodes and their edges would be lost.
export function unknownNodeTypes(g: FlowGraph, types: Pick<FlowNodeType, 'type'>[]): string[] {
  const known = new Set(types.map(t => t.type))
  return [...new Set(g.nodes.map(n => n.type).filter(t => !known.has(t)))]
}

// The default values of a new node, so required fields with a default validate at once.
export function defaultConfig(t: FlowNodeType): Record<string, unknown> {
  const config: Record<string, unknown> = {}
  for (const f of t.fields ?? []) if (f.default !== undefined) config[f.name] = f.default
  return config
}

// One line under a node's title: what this node is set to do.
export function nodeSummary(t: FlowNodeType, config: Record<string, unknown>): string {
  for (const f of t.fields ?? []) {
    const v = config[f.name]
    if (v === undefined || v === '' || v === 'any' || v === null || f.kind === 'bool') continue
    return String(v).replace(/\s+/g, ' ').slice(0, 40)
  }
  return ''
}

// Values travel on the wires: a node gives its answer through the output it takes, and that
// answer reaches the input of the nodes connected to it and starts them. There is no "next".

// The outputs of a node type with what each carries. A plugin's node may only list names.
export function outputPins(t: Pick<FlowNodeType, 'ports' | 'outputs'>): FlowPin[] {
  if (t.outputs?.length) return t.outputs
  return (t.ports ?? []).map(name => ({ name, type: name === 'failed' ? 'text' : 'any' }))
}

// The input of a node type: every node but a trigger has one.
export function inputPin(t: Pick<FlowNodeType, 'category' | 'input'>): FlowPin | null {
  if (t.category === 'trigger') return null
  return t.input ?? { name: 'input', type: 'any' }
}

// litegraph's slot type for a pin. An empty type takes anything, and litegraph refuses to
// wire two different named types: text into a JSON input is refused while dragging.
export function slotType(pin: FlowPin): string {
  return pin.type === 'any' || !pin.type ? '' : pin.type
}

// The colors of the wires, by what they carry.
export const PIN_COLORS: Record<string, string> = { text: '#c9a45c', json: '#5ca8c9', any: '#9a97a0' }

// A value from a run, as the panel shows it: text as it is, anything else as JSON.
export function valueText(v: unknown): string {
  if (v === undefined || v === null) return ''
  if (typeof v === 'string') return v
  return JSON.stringify(v, null, 2)
}

// The name each output shows: a field can name one (an AI evaluate's answers, a Switch's cases).
export function portLabels(t: FlowNodeType, config: Record<string, unknown>): Map<string, string> {
  const out = new Map<string, string>()
  for (const f of t.fields ?? []) {
    const v = config[f.name]
    if (!f.port_label) continue
    out.set(f.port_label, typeof v === 'string' && v.trim() ? v.trim() : 'unused')
  }
  return out
}

// The status color of a node in a run: the newest step of the node decides it.
export const STEP_COLORS: Record<string, string> = {
  pending: '#6b6771',
  running: '#d9a441',
  waiting: '#4f8fd9',
  succeeded: '#2f9e7a',
  failed: '#d9534f',
  cancelled: '#55525c',
}

export function nodeStatuses(steps: { node_id: string; status: string }[]): Map<string, string> {
  const out = new Map<string, string>()
  for (const s of steps) out.set(s.node_id, s.status)
  return out
}
