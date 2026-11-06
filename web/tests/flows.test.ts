import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { createActor, fromPromise, waitFor } from 'xstate'
import { buildCardMachine, canMoveCard, cardState } from '../src/lib/cardMachine.ts'
import { editorMachine } from '../src/components/Flows/editorMachine.ts'
import { canvasType, defaultConfig, flowType, nodeStatuses, nodeSummary, parseFlowGraph, portLabels, serializeCanvas, unknownNodeTypes } from '../src/components/Flows/flowGraph.ts'

const fixture = (name: string) => JSON.parse(readFileSync(new URL(`./fixtures/${name}`, import.meta.url), 'utf8'))

test('the card machine gives the same answers as the hub', () => {
  // card-machine.json is checked against the Go table by TestCardMachineMatchesTheWebFixture
  const machine = buildCardMachine(fixture('card-machine.json'))
  for (const c of fixture('card-machine-cases.json')) {
    assert.equal(canMoveCard(machine, c.from, c.to, c.context), c.allowed, `${c.from} -> ${c.to} ${JSON.stringify(c.context)}`)
  }
})

test('a list without a role is the custom state', () => {
  assert.equal(cardState(''), 'custom')
  assert.equal(cardState('todo'), 'todo')
})

test('serializing keeps ports, positions and config, and only what the hub stores', () => {
  const nodes = [
    { id: 1, type: 'trigger/trigger.manual', pos: [10.4, 20.6], properties: { flowId: 'n1', config: {} }, outputs: [{ name: 'next', links: [7] }], extra: 'ignored' },
    { id: 2, type: 'control/control.if', pos: [100, 20], properties: { flowId: 'n2', config: { condition: 'true' } }, outputs: [{ name: 'true', links: [8, 9] }, { name: 'false', links: null }] },
    { id: 3, type: 'action/action.comment', pos: [200, 0], properties: { flowId: 'n3', config: { text: 'a' } }, outputs: [{ name: 'next', links: [] }] },
    { id: 4, type: 'control/control.join', pos: [200, 50], properties: { flowId: 'n4' }, outputs: null },
  ]
  const links = { 7: { target_id: 2 }, 8: { target_id: 3 }, 9: { target_id: 4 } }
  assert.deepEqual(serializeCanvas(nodes, links), {
    nodes: [
      { id: 'n1', type: 'trigger.manual', x: 10, y: 21, config: {} },
      { id: 'n2', type: 'control.if', x: 100, y: 20, config: { condition: 'true' } },
      { id: 'n3', type: 'action.comment', x: 200, y: 0, config: { text: 'a' } },
      { id: 'n4', type: 'control.join', x: 200, y: 50, config: {} },
    ],
    edges: [
      { from: 'n1', port: 'next', to: 'n2' },
      { from: 'n2', port: 'true', to: 'n3' },
      { from: 'n2', port: 'true', to: 'n4' },
    ],
  })
})

test('a link to a node that is gone is dropped', () => {
  const nodes = [{ id: 1, type: 'a/a.b', pos: [0, 0], properties: { flowId: 'n1' }, outputs: [{ name: 'next', links: [5, 6] }] }]
  assert.deepEqual(serializeCanvas(nodes, { 5: { target_id: 99 } }).edges, [])
})

test('canvas types go back and forth', () => {
  const t = { category: 'action' as const, type: 'action.comment' }
  assert.equal(canvasType(t), 'action/action.comment')
  assert.equal(flowType(canvasType(t)), 'action.comment')
})

test('helpers', () => {
  assert.deepEqual(parseFlowGraph(null), { nodes: [], edges: [] })
  assert.deepEqual(parseFlowGraph('{"nodes":[{"id":"a"}]}'), { nodes: [{ id: 'a' }], edges: [] })
  const type = {
    type: 'x', title: 'X', category: 'control' as const, description: '', ports: [],
    fields: [{ name: 'a', label: 'A', kind: 'select' as const, default: 'any' }, { name: 'b', label: 'B', kind: 'text' as const }, { name: 'c', label: 'C', kind: 'number' as const, default: 0 }],
  }
  assert.deepEqual(defaultConfig(type), { a: 'any', c: 0 })
  assert.equal(nodeSummary(type, { a: 'any', b: 'two\nlines' }), 'two lines')
  assert.equal(nodeSummary(type, {}), '')
  assert.equal(nodeStatuses([{ node_id: 'a', status: 'failed' }, { node_id: 'a', status: 'succeeded' }]).get('a'), 'succeeded')
})

test('a checkbox is not what a node is set to do', () => {
  const type = {
    type: 'action.merge', title: 'Merge', category: 'action' as const, description: '', ports: [],
    fields: [{ name: 'keep', label: 'K', kind: 'bool' as const, default: true }, { name: 'how', label: 'H', kind: 'select' as const }],
  }
  assert.equal(nodeSummary(type, { keep: true, how: 'squash' }), 'squash')
  assert.deepEqual(defaultConfig(type), { keep: true })
})

test('an output shows the answer it stands for, and says so when it has none', () => {
  const type = {
    type: 'action.ai_evaluate', title: 'AI', category: 'action' as const, description: '', ports: ['outcome1', 'outcome2', 'outcome3'],
    fields: [
      { name: 'outcome1', label: 'A', kind: 'text' as const, port_label: 'outcome1' },
      { name: 'outcome2', label: 'B', kind: 'text' as const, port_label: 'outcome2' },
      { name: 'outcome3', label: 'C', kind: 'text' as const, port_label: 'outcome3' },
      { name: 'model', label: 'M', kind: 'select' as const },
    ],
  }
  const labels = portLabels(type, { outcome1: ' low ', outcome2: 'high', outcome3: '', model: 'x' })
  assert.deepEqual([...labels], [['outcome1', 'low'], ['outcome2', 'high'], ['outcome3', 'unused']])
})

// ---- the editor machine ----

function editor(opts: { save?: () => Promise<void>; publish?: () => Promise<{ message: string }[]> } = {}) {
  const calls = { save: 0, publish: 0 }
  const machine = editorMachine.provide({
    delays: { autosave: 10 },
    actors: {
      save: fromPromise<void>(async () => { calls.save++; await opts.save?.() }),
      publish: fromPromise(async () => { calls.publish++; return (await opts.publish?.()) ?? [] }),
    },
  })
  const actor = createActor(machine).start()
  actor.send({ type: 'OPEN' })
  return { actor, calls }
}

const settled = (actor: ReturnType<typeof editor>['actor']) => waitFor(actor, s => s.matches('clean'), { timeout: 1000 })

test('a change saves itself, once, after a pause', async () => {
  const { actor, calls } = editor()
  actor.send({ type: 'CHANGE' })
  actor.send({ type: 'CHANGE' })
  actor.send({ type: 'CHANGE' })
  assert.ok(actor.getSnapshot().matches('dirty'))
  await settled(actor)
  assert.equal(calls.save, 1)
  assert.equal(actor.getSnapshot().context.unpublished, true)
  actor.stop()
})

test('an edit during a save is not lost', async () => {
  let release = () => {}
  const first = new Promise<void>(r => { release = r })
  let n = 0
  const { actor, calls } = editor({ save: () => (n++ === 0 ? first : Promise.resolve()) })
  actor.send({ type: 'CHANGE' })
  await waitFor(actor, s => s.matches('saving'), { timeout: 1000 })
  actor.send({ type: 'CHANGE' }) // the save in flight does not hold this
  release()
  await waitFor(actor, s => s.matches('dirty'), { timeout: 1000 })
  await settled(actor)
  assert.equal(calls.save, 2)
  actor.stop()
})

test('publishing saves the draft first', async () => {
  const order: string[] = []
  const { actor } = editor({
    save: async () => { order.push('save') },
    publish: async () => { order.push('publish'); return [] },
  })
  actor.send({ type: 'CHANGE' })
  actor.send({ type: 'PUBLISH' })
  await waitFor(actor, s => s.matches('publishing'), { timeout: 1000 })
  await settled(actor)
  assert.deepEqual(order, ['save', 'publish'])
  assert.equal(actor.getSnapshot().context.unpublished, false)
  actor.stop()
})

test('problems stop a publish and stay visible', async () => {
  const { actor } = editor({ publish: async () => [{ message: 'needs a trigger' }] })
  actor.send({ type: 'CHANGE' })
  await settled(actor)
  actor.send({ type: 'PUBLISH' })
  await waitFor(actor, s => s.matches('clean') && s.context.problems.length > 0, { timeout: 1000 })
  assert.equal(actor.getSnapshot().context.problems[0].message, 'needs a trigger')
  assert.equal(actor.getSnapshot().context.unpublished, true)
  actor.stop()
})

test('a failed save stays unsaved and does not retry on its own', async () => {
  const { actor, calls } = editor({ save: async () => { throw new Error('offline') } })
  actor.send({ type: 'CHANGE' })
  await waitFor(actor, s => s.matches('saveFailed'), { timeout: 1000 })
  assert.equal(actor.getSnapshot().context.error, 'offline')
  await new Promise(r => setTimeout(r, 40))
  assert.equal(calls.save, 1)
  actor.stop()
})

test('a graph with nodes this hub does not offer is reported, each type once', () => {
  const node = (id: string, type: string) => ({ id, type, x: 0, y: 0, config: {} })
  const types = [{ type: 'action.comment' }]
  const graph = { nodes: [node('n1', 'action.git_status'), node('n2', 'action.comment'), node('n3', 'action.git_status')], edges: [] }
  assert.deepEqual(unknownNodeTypes(graph, types), ['action.git_status'])
  assert.deepEqual(unknownNodeTypes({ nodes: [node('n2', 'action.comment')], edges: [] }, types), [])
})

test('a past run locks the canvas: edits do nothing until going back', () => {
  const { actor, calls } = editor()
  actor.send({ type: 'VIEW_RUN' })
  actor.send({ type: 'CHANGE' })
  actor.send({ type: 'PUBLISH' })
  assert.ok(actor.getSnapshot().matches('viewingRun'))
  assert.equal(calls.save + calls.publish, 0)
  actor.send({ type: 'BACK' })
  assert.ok(actor.getSnapshot().matches('clean'))
  actor.stop()
})
