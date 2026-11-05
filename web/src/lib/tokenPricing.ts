// Token pricing in USD per million tokens (MTok)
// Source: Anthropic pricing page (Apr 2026)
interface ModelPricing {
  inputPerMTok: number
  outputPerMTok: number
}

const PRICING: Record<string, ModelPricing> = {
  'claude-haiku-4-5':              { inputPerMTok: 0.80,  outputPerMTok: 4.00  },
  'claude-haiku-4-5-20251001':     { inputPerMTok: 0.80,  outputPerMTok: 4.00  },
  'claude-sonnet-4-5':             { inputPerMTok: 3.00,  outputPerMTok: 15.00 },
  'claude-sonnet-4-6':             { inputPerMTok: 3.00,  outputPerMTok: 15.00 },
  'claude-opus-4-5':               { inputPerMTok: 15.00, outputPerMTok: 75.00 },
  'claude-opus-4-6':               { inputPerMTok: 15.00, outputPerMTok: 75.00 },
}

function getModelPricing(model: string): ModelPricing {
  // Exact match first
  if (PRICING[model]) return PRICING[model]
  // Fuzzy: haiku / sonnet / opus
  const lower = model.toLowerCase()
  if (lower.includes('haiku'))  return { inputPerMTok: 0.80,  outputPerMTok: 4.00  }
  if (lower.includes('sonnet')) return { inputPerMTok: 3.00,  outputPerMTok: 15.00 }
  if (lower.includes('opus'))   return { inputPerMTok: 15.00, outputPerMTok: 75.00 }
  // Unknown model — default to Sonnet pricing
  return { inputPerMTok: 3.00, outputPerMTok: 15.00 }
}

/**
 * Estimate cost in USD for a set of token counts and a model.
 * Cache read tokens cost 10% of input price.
 * Cache write (creation) tokens cost 125% of input price (the 5-minute cache; the 1-hour
 * cache writes at 200%, and the usage rows do not say which one was used).
 */
export function estimateCost(
  model: string,
  inputTokens: number,
  outputTokens: number,
  cacheReadTokens = 0,
  cacheWriteTokens = 0,
): number {
  const pricing = getModelPricing(model)
  const perTok = pricing.inputPerMTok / 1_000_000
  const outPerTok = pricing.outputPerMTok / 1_000_000
  return (
    inputTokens     * perTok +
    outputTokens    * outPerTok +
    cacheReadTokens  * perTok * 0.10 +
    cacheWriteTokens * perTok * 1.25
  )
}

// 950, 12.3k, 4.2M
export function formatTokens(n: number): string {
  if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M'
  if (n >= 1_000) return (n / 1_000).toFixed(1) + 'k'
  return String(n)
}

export function formatCost(usd: number): string {
  if (usd === 0) return '$0.00'
  if (usd < 0.001) return `<$0.001`
  if (usd < 1) return `$${usd.toFixed(4)}`
  return `$${usd.toFixed(2)}`
}
