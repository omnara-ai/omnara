import type { DiscoveredModelPricing } from '@omnara/sdk'

import { formatUsdPerMillion } from '@/lib/format'

export function ModelPricingSummary({
  pricing,
  className,
}: {
  pricing: DiscoveredModelPricing | undefined
  className?: string
}) {
  if (!pricing) return <span className={className}>—</span>
  return (
    <span className={className}>
      {formatUsdPerMillion(pricing.input_usd_per_million)} in ·{' '}
      {formatUsdPerMillion(pricing.output_usd_per_million)} out
    </span>
  )
}
