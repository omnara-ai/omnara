import type { MachinePool } from '@omnara/sdk'
import { z } from 'zod'

export type ProviderOptions = MachinePool['default_machine_provider_options']

const stringOptions = z.record(z.string(), z.string().optional().catch(undefined))

const optionSummaries = z.record(
  z.string(),
  z.string().catch(({ value }) => JSON.stringify(value)),
)

export function providerOptionStrings(options: ProviderOptions) {
  return stringOptions.parse(options)
}

export function providerOptionSummaries(options: ProviderOptions) {
  return optionSummaries.parse(options)
}
