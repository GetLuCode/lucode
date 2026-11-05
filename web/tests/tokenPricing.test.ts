import test from 'node:test'
import assert from 'node:assert/strict'
import { estimateCost, estimateUsageCost, formatTokens } from '../src/lib/tokenPricing.ts'

const near = (a: number, b: number) => assert.ok(Math.abs(a - b) < 1e-9, `${a} != ${b}`)

test('cache reads cost 10% and cache writes 125% of the input price', () => {
  // sonnet: $3 per million input tokens
  near(estimateCost('claude-sonnet-4-6', 1_000_000, 0), 3)
  near(estimateCost('claude-sonnet-4-6', 0, 0, 1_000_000), 0.3)
  near(estimateCost('claude-sonnet-4-6', 0, 0, 0, 1_000_000), 3.75)
})

test('writes to the 1 hour cache cost 200% of the input price', () => {
  near(estimateCost('claude-sonnet-4-6', 0, 0, 0, 0, 1_000_000), 6)
  near(estimateCost('claude-sonnet-4-6', 0, 0, 0, 1_000_000, 1_000_000), 3.75 + 6)
})

test('a usage row prices its 1 hour and 5 minute cache writes apart', () => {
  // 1M written: 400k with the 1h cache (2x) and the other 600k as 5m writes (1.25x)
  const usage = { input_tokens: 0, output_tokens: 0, cache_creation_input_tokens: 1_000_000, cache_creation_1h_input_tokens: 400_000 }
  near(estimateUsageCost('claude-sonnet-4-6', usage), 0.4 * 6 + 0.6 * 3.75)
  // rows from before the split was reported have no 1h tokens: all 5m
  near(estimateUsageCost('claude-sonnet-4-6', { ...usage, cache_creation_1h_input_tokens: undefined }), 3.75)
  // a 1h count above the total written cannot be billed twice
  near(estimateUsageCost('claude-sonnet-4-6', { ...usage, cache_creation_1h_input_tokens: 9_000_000 }), 6)
})

test('output tokens are priced separately', () => {
  near(estimateCost('claude-sonnet-4-6', 0, 1_000_000), 15)
})

test('formatTokens abbreviates thousands and millions', () => {
  assert.equal(formatTokens(950), '950')
  assert.equal(formatTokens(12_300), '12.3k')
  assert.equal(formatTokens(4_200_000), '4.2M')
})
