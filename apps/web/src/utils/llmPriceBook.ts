
// LLM Price Book - Cost per million tokens
export interface ModelPricing {
  input_cost_per_million_tokens: number
  output_cost_per_million_tokens: number
}

export const LLM_PRICE_BOOK: Record<string, ModelPricing> = {
  "claude-3-5-sonnet-20240620": {
    input_cost_per_million_tokens: 3.00,
    output_cost_per_million_tokens: 15.00
  },
  "claude-3-5-haiku-20241022": {
    input_cost_per_million_tokens: 0.25,
    output_cost_per_million_tokens: 1.25
  },
  "gpt-4o": {
    input_cost_per_million_tokens: 5.00,
    output_cost_per_million_tokens: 15.00
  },
  "gpt-4o-mini": {
    input_cost_per_million_tokens: 0.15,
    output_cost_per_million_tokens: 0.60
  },
  "gpt-4.1-2025-04-14": {
    input_cost_per_million_tokens: 5.00,
    output_cost_per_million_tokens: 15.00
  },
  "o3-2025-04-16": {
    input_cost_per_million_tokens: 20.00,
    output_cost_per_million_tokens: 80.00
  },
  "o4-mini-2025-04-16": {
    input_cost_per_million_tokens: 0.30,
    output_cost_per_million_tokens: 1.20
  }
}

export function calculateLLMCost(
  modelName: string,
  promptTokens: number,
  completionTokens: number
): number {
  const pricing = LLM_PRICE_BOOK[modelName]
  
  if (!pricing) {
    console.warn(`No pricing found for model: ${modelName}`)
    return 0
  }

  const inputCost = (promptTokens / 1_000_000) * pricing.input_cost_per_million_tokens
  const outputCost = (completionTokens / 1_000_000) * pricing.output_cost_per_million_tokens
  
  return inputCost + outputCost
}

export function formatCost(costUsd: number): string {
  if (costUsd === 0) return '$0.00'
  if (costUsd < 0.01) return `$${costUsd.toFixed(6)}`
  return `$${costUsd.toFixed(4)}`
}
