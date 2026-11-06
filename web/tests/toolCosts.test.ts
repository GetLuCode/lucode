import test from 'node:test'
import assert from 'node:assert/strict'
import { totalsByTool, toolTotalCost, toolLabel } from '../src/lib/toolCosts.ts'

const near = (a: number, b: number) => assert.ok(Math.abs(a - b) < 1e-9, `${a} != ${b}`)

const row = (over: Record<string, unknown>) => ({
  tool: 'Grep', model: 'claude-sonnet-4-6', calls: 1, errors: 0, result_tokens: 0, unmeasured: 0,
  output_tokens: 0, written_1h: 0, written_5m: 0, uncached: 0, carried_read: 0, ...over,
})

test('a tool is priced in its three parts, each kind of cache write at its own price', () => {
  // sonnet: $3 input, $15 output per million
  const [t] = totalsByTool([row({ output_tokens: 1e6, written_5m: 1e6, written_1h: 1e6, carried_read: 1e6 })] as never)
  near(t.askCost, 15)
  near(t.writeCost, 3.75 + 6) // 5 minute writes at 1.25x, 1 hour at 2x
  near(t.readCost, 0.3)
  near(toolTotalCost(t), 15 + 3.75 + 6 + 0.3)
})

test('rows of one tool in different models add up, each priced with its own model', () => {
  const rows = [
    row({ calls: 2, errors: 1, output_tokens: 1e6 }),
    row({ calls: 3, model: 'claude-haiku-4-5', output_tokens: 1e6 }), // haiku: $4 output
    row({ tool: 'Read', calls: 1 }),
  ]
  const totals = totalsByTool(rows as never)
  const grep = totals.find(t => t.tool === 'Grep')!
  assert.equal(totals.length, 2)
  assert.equal(grep.calls, 5)
  assert.equal(grep.errors, 1)
  near(grep.askCost, 15 + 4)
})

test('lucode tools are shown without the mcp prefix', () => {
  assert.equal(toolLabel('mcp__lucode__task_done'), 'lucode:task_done')
  assert.equal(toolLabel('Bash'), 'Bash')
})
