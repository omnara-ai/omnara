import type { DiscoveredModelPricing } from '@omnara/sdk'

import { formatUsdPerMillion } from '@/lib/format'

export function ModelPricingSummary({
  pricing,
  className,
}: {
  pricing: DiscoveredModelPricing | undefined
  className?: string
}) {
  return (
    <span className={className}>
      {pricing
        ? `${formatUsdPerMillion(pricing.input_usd_per_million)} in · ${formatUsdPerMillion(pricing.output_usd_per_million)} out`
        : '—'}
    </span>
  )
}
