import type { CreateMachinePoolRequest } from '@omnara/sdk'

interface MachinePoolProviderDefinition {
  label: string
  resource: {
    key: string
    label: string
    placeholder: string
    description?: string
    descriptionHref?: string
  }
  location: {
    key: string
    label: string
    placeholder: string
    defaultValue: string
  }
  requiresWorkspace: boolean
  resources: {
    cpu: MachinePoolResourceMode
    memoryMb: MachinePoolResourceMode
  }
}

export type MachinePoolProvider = CreateMachinePoolRequest['provider']
export type MachinePoolResourceMode = 'configured' | 'provider-resolved' | 'unsupported'

export const machinePoolProviderDefinitions: Record<
  MachinePoolProvider,
  MachinePoolProviderDefinition
> = {
  unikraft: {
    label: 'Unikraft',
    resource: {
      key: 'image',
      label: 'Image',
      placeholder: 'omnara/agent-sandbox',
      description:
        'Subprocess support is required when using custom images; base-compat is the recommended runtime base.',
      descriptionHref: 'https://unikraft.com/docs/platform/images',
    },
    location: {
      key: 'metro',
      label: 'Metro',
      placeholder: 'sfo',
      defaultValue: 'sfo',
    },
    requiresWorkspace: false,
    resources: { cpu: 'configured', memoryMb: 'configured' },
  },
  blaxel: {
    label: 'Blaxel',
    resource: {
      key: 'image',
      label: 'Image',
      placeholder: 'omnara/agent-sandbox',
      description: "Custom images must include Blaxel's sandbox-api binary.",
      descriptionHref: 'https://docs.blaxel.ai/Sandboxes/Templates',
    },
    location: {
      key: 'region',
      label: 'Region',
      placeholder: 'us-pdx-1',
      defaultValue: 'us-pdx-1',
    },
    requiresWorkspace: true,
    resources: { cpu: 'unsupported', memoryMb: 'configured' },
  },
  daytona: {
    label: 'Daytona',
    resource: {
      key: 'snapshot',
      label: 'Snapshot',
      placeholder: 'Snapshot name',
    },
    location: {
      key: 'target',
      label: 'Target',
      placeholder: 'us',
      defaultValue: 'us',
    },
    requiresWorkspace: false,
    resources: { cpu: 'provider-resolved', memoryMb: 'provider-resolved' },
  },
}

export function isMachinePoolProvider(value: string): value is MachinePoolProvider {
  return Object.hasOwn(machinePoolProviderDefinitions, value)
}
