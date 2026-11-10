import type { UsageCostTotals } from '@omnara/sdk'

export function totalCostUsd(cost: UsageCostTotals) {
  return Number(cost.provider_reported_usd) + Number(cost.estimated_usd)
}

export function unpricedModelCalls(modelCalls: number, cost: UsageCostTotals) {
  return Math.max(
    0,
    modelCalls - cost.model_calls_with_reported_cost - cost.model_calls_with_estimated_cost,
  )
}

const usdFractionDigits = 15

function usdUnits(decimal: string) {
  const [whole = '0', fraction = ''] = decimal.split('.')
  return BigInt(whole + fraction.padEnd(usdFractionDigits, '0').slice(0, usdFractionDigits))
}

export function spendReachesBudget(spent: string, budget: string) {
  return usdUnits(spent) >= usdUnits(budget)
}
