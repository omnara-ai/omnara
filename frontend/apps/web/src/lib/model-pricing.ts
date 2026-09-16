import type { DiscoveredModelPricing } from '@omnara/sdk'

import type { DetailItem } from '@/components/data-table/DetailList'
import { formatUsdPerMillion } from '@/lib/format'

export function modelPricingDetailItems(pricing: DiscoveredModelPricing | undefined): DetailItem[] {
  if (!pricing) return []
  return [
    {
      label: 'Input price',
      value: `${formatUsdPerMillion(pricing.input_usd_per_million)} / 1M tokens`,
    },
    {
      label: 'Cache read price',
      value: pricing.cache_read_input_usd_per_million
        ? `${formatUsdPerMillion(pricing.cache_read_input_usd_per_million)} / 1M tokens`
        : undefined,
    },
    {
      label: 'Cache write price',
      value: pricing.cache_write_input_usd_per_million
        ? `${formatUsdPerMillion(pricing.cache_write_input_usd_per_million)} / 1M tokens`
        : undefined,
    },
    {
      label: 'Output price',
      value: `${formatUsdPerMillion(pricing.output_usd_per_million)} / 1M tokens`,
    },
  ]
}
