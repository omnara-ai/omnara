import type { DiscoveredProviderModel } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import { catalogPricing } from './model-providers'

function model(slug: string, input = '1'): DiscoveredProviderModel {
  return {
    slug,
    display_name: slug,
    context_window_tokens: 1000,
    pricing: {
      input_usd_per_million: input,
      cache_read_input_usd_per_million: '',
      cache_write_input_usd_per_million: '',
      output_usd_per_million: '2',
    },
  }
}

describe('catalogPricing', () => {
  const catalog = [
    model('qwen/qwen3.8-max-0902', '3'),
    model('qwen/qwen3.8-max-0601', '4'),
    model('qwen/qwen3.7-plus', '5'),
    model('qwen/qwen3.7-plus-preview', '6'),
  ]

  it('prefers the exact slug', () => {
    expect(catalogPricing(catalog, 'qwen/qwen3.7-plus')?.input_usd_per_million).toBe('5')
  })

  it('falls back to the first dated variant of the slug', () => {
    expect(catalogPricing(catalog, 'qwen/qwen3.8-max')?.input_usd_per_million).toBe('3')
  })

  it('does not match unrelated slugs', () => {
    expect(catalogPricing(catalog, 'qwen/qwen3.8')).toBeUndefined()
    expect(catalogPricing(catalog, 'qwen/qwen3.8-ma')).toBeUndefined()
    expect(catalogPricing(catalog, 'qwen/qwen3.7')).toBeUndefined()
  })
})
