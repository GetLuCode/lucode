import test from 'node:test'
import assert from 'node:assert/strict'
import { estimateCost, formatTokens } from '../src/lib/tokenPricing.ts'

const near = (a: number, b: number) => assert.ok(Math.abs(a - b) < 1e-9, `${a} != ${b}`)

test('cache reads cost 10% and cache writes 125% of the input price', () => {
  // sonnet: $3 per million input tokens
  near(estimateCost('claude-sonnet-4-6', 1_000_000, 0), 3)
  near(estimateCost('claude-sonnet-4-6', 0, 0, 1_000_000), 0.3)
  near(estimateCost('claude-sonnet-4-6', 0, 0, 0, 1_000_000), 3.75)
})

test('output tokens are priced separately', () => {
  near(estimateCost('claude-sonnet-4-6', 0, 1_000_000), 15)
})

test('formatTokens abbreviates thousands and millions', () => {
  assert.equal(formatTokens(950), '950')
  assert.equal(formatTokens(12_300), '12.3k')
  assert.equal(formatTokens(4_200_000), '4.2M')
})
