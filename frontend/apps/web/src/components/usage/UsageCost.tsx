import type { UsageCostTotals } from '@omnara/sdk'

import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { totalCostUsd, unpricedModelCalls } from '@/components/usage/cost-totals'
import { formatCount, formatUsd } from '@/lib/format'

export function UsageCost({
  modelCalls,
  cost,
  format = formatUsd,
}: {
  modelCalls: number
  cost: UsageCostTotals
  format?: (decimal: string) => string
}) {
  const total = String(totalCostUsd(cost))
  const estimatedCalls = cost.model_calls_with_estimated_cost
  const missing = unpricedModelCalls(modelCalls, cost)
  if (missing <= 0 && estimatedCalls <= 0) {
    return <span className="tabular-nums">{format(total)}</span>
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-help tabular-nums">
          {format(total)}
          <span className="text-muted-foreground"> *</span>
        </span>
      </TooltipTrigger>
      <TooltipContent side="bottom" className="flex max-w-xs flex-col gap-1 text-left">
        {estimatedCalls > 0 && (
          <span>
            {format(cost.estimated_usd)} for {formatCount(estimatedCalls)}{' '}
            {estimatedCalls === 1 ? 'call is' : 'calls is'} estimated from list prices, so the bill
            may be higher or lower.
          </span>
        )}
        {missing > 0 && (
          <span>
            {formatCount(missing)} of {formatCount(modelCalls)} calls couldn't be priced and aren't
            included.
          </span>
        )}
      </TooltipContent>
    </Tooltip>
  )
}
