import type { ToolCostRow } from '../api'
import { estimateCost } from './tokenPricing.ts' // the extension: Node's test runner needs it

export interface ToolTotals {
  tool: string
  calls: number
  errors: number
  unmeasured: number
  resultTokens: number
  carriedRead: number
  outputTokens: number
  // dollars: asking for the tool, writing its result to the cache, re-reading it in later calls
  askCost: number
  writeCost: number
  readCost: number
}

export const toolTotalCost = (t: ToolTotals) => t.askCost + t.writeCost + t.readCost

// The tool rows come per model; add them up per tool, priced model by model.
export function totalsByTool(rows: ToolCostRow[]): ToolTotals[] {
  const by = new Map<string, ToolTotals>()
  for (const r of rows) {
    const t = by.get(r.tool) ?? {
      tool: r.tool, calls: 0, errors: 0, unmeasured: 0, resultTokens: 0, carriedRead: 0, outputTokens: 0,
      askCost: 0, writeCost: 0, readCost: 0,
    }
    t.calls += r.calls
    t.errors += r.errors
    t.unmeasured += r.unmeasured
    t.resultTokens += r.result_tokens
    t.carriedRead += r.carried_read
    t.outputTokens += r.output_tokens
    t.askCost += estimateCost(r.model, 0, r.output_tokens)
    t.writeCost += estimateCost(r.model, r.uncached, 0, 0, r.written_5m, r.written_1h)
    t.readCost += estimateCost(r.model, 0, 0, r.carried_read)
    by.set(r.tool, t)
  }
  return [...by.values()]
}

// lucode's own tools are named mcp__lucode__task_done: show lucode:task_done
export const toolLabel = (name: string) => name.replace(/^mcp__lucode__/, 'lucode:')
