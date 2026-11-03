import type { UsageCostTotals } from '@omnara/sdk'

import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { formatCount, formatUsd } from '@/lib/format'

export function ReportedCost({
  modelCalls,
  cost,
  format = formatUsd,
}: {
  modelCalls: number
  cost: UsageCostTotals
  format?: (decimal: string) => string
}) {
  const missing = modelCalls - cost.model_calls_with_reported_cost
  if (missing <= 0) {
    return <span className="tabular-nums">{format(cost.provider_reported_usd)}</span>
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-help tabular-nums">
          {format(cost.provider_reported_usd)}
          <span className="text-muted-foreground"> *</span>
        </span>
      </TooltipTrigger>
      <TooltipContent side="bottom" className="max-w-xs text-left">
        {formatCount(missing)} of {formatCount(modelCalls)} calls reported no cost, so the actual
        cost is higher than shown.
      </TooltipContent>
    </Tooltip>
  )
}
