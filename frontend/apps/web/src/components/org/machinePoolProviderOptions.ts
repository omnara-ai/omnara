import type { MachinePoolFormValues } from './MachinePoolDialogState'
import { machinePoolProviderDefinitions } from './machinePoolProviders'

/** The rootfs a new CreateOS pool starts from; the machine size decides cpu and memory. */
export const createosRootFS = 'devbox:1'

/** The machine provider options a pool is created with, keyed by the provider definition. */
export function machineProviderOptions(values: MachinePoolFormValues) {
  const definition = machinePoolProviderDefinitions[values.provider]
  return {
    [definition.resource.key]: values.image.trim(),
    [definition.location.key]: values.location.trim(),
    ...rootfsOption(values),
  }
}

/** The CreateOS rootfs entry, absent for other providers and for an empty draft. */
export function rootfsOption(values: MachinePoolFormValues) {
  const rootfs = values.rootfs.trim()
  if (values.provider !== 'createos' || rootfs === '') return {}
  return { rootfs }
}
