import type { UsageCostTotals } from '@omnara/sdk'

import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { formatCount, formatUsd } from '@/lib/format'

export function ReportedCost({ modelCalls, cost }: { modelCalls: number; cost: UsageCostTotals }) {
  const missing = modelCalls - cost.model_calls_with_reported_cost
  if (missing <= 0) {
    return <span className="tabular-nums">{formatUsd(cost.provider_reported_usd)}</span>
  }
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span className="cursor-help tabular-nums">
          {formatUsd(cost.provider_reported_usd)}
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
