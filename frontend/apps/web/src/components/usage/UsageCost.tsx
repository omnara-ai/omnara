import type { UsageCostTotals } from '@omnara/sdk'

import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
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
  const missing = modelCalls - cost.model_calls_with_effective_cost
  if (missing <= 0) {
    return <span className="tabular-nums">{format(cost.effective_usd)}</span>
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-help tabular-nums">
          {format(cost.effective_usd)}
          <span className="text-muted-foreground"> *</span>
        </span>
      </TooltipTrigger>
      <TooltipContent side="bottom" className="max-w-xs text-left">
        {formatCount(missing)} of {formatCount(modelCalls)} calls couldn't be priced and aren't
        included.
      </TooltipContent>
    </Tooltip>
  )
}
