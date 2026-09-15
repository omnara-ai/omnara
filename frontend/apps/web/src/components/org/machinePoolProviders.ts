import type { CreateMachinePoolRequest, MachinePool } from '@omnara/sdk'

import { providerOptionStrings } from '@/lib/provider-options'

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
    required: boolean
    supported?: boolean
  }
  scope?: {
    key: string
    label: string
    placeholder: string
    required: boolean
    description?: string
  }
  credential?: {
    label: string
    placeholder: string
    emptyDescription: string
    defaultSecretName: string
    secretValuePlaceholder: string
  }
  resources: {
    cpu: MachinePoolResourceMode
    memoryMb: MachinePoolResourceMode
    defaultCpu?: string
    defaultMemoryGb?: string
  }
}

export type MachinePoolProvider = CreateMachinePoolRequest['provider']
export type MachinePoolResourceMode = 'configured' | 'provider-resolved' | 'unsupported'

export function machinePoolScopeValue(
  provider: MachinePoolProvider,
  config: MachinePool['provider_config'],
) {
  const scope = machinePoolProviderDefinitions[provider].scope
  const value = scope ? providerOptionStrings(config)[scope.key] : undefined
  if (provider !== 'modal') return value
  const app = value?.trim()
  return app === undefined || app === '' ? 'omnara' : app
}

export function machinePoolCoreProviderOptions(
  provider: MachinePoolProvider,
  resource: string,
  location: string,
) {
  const definition = machinePoolProviderDefinitions[provider]
  const includeLocation =
    definition.location.supported !== false &&
    (definition.location.required || location.trim() !== '')
  const options = { [definition.resource.key]: resource.trim() }
  if (includeLocation) {
    options[definition.location.key] = location.trim()
  }
  return options
}

const unikraft: MachinePoolProviderDefinition = {
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
    required: true,
  },
  resources: { cpu: 'configured', memoryMb: 'configured' },
}

const blaxel: MachinePoolProviderDefinition = {
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
    required: true,
  },
  scope: {
    key: 'workspace',
    label: 'Workspace',
    placeholder: 'Workspace name',
    required: true,
  },
  resources: { cpu: 'unsupported', memoryMb: 'configured' },
}

const daytona: MachinePoolProviderDefinition = {
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
    required: true,
  },
  resources: { cpu: 'provider-resolved', memoryMb: 'provider-resolved' },
}

const freestyle: MachinePoolProviderDefinition = {
  label: 'Freestyle',
  resource: {
    key: 'snapshot',
    label: 'Snapshot',
    placeholder: 'freestyle/ubuntu-sm',
    description: 'The configured vCPU and memory must be at least the snapshot size.',
    descriptionHref: 'https://www.freestyle.sh/docs/vms/base-snapshots',
  },
  location: {
    key: '',
    label: '',
    placeholder: '',
    defaultValue: '',
    required: false,
    supported: false,
  },
  resources: {
    cpu: 'configured',
    memoryMb: 'configured',
    defaultCpu: '2',
    defaultMemoryGb: '4',
  },
}

const modal: MachinePoolProviderDefinition = {
  label: 'Modal',
  resource: {
    key: 'image',
    label: 'Image',
    placeholder: 'omnara/agent-sandbox',
    description: 'Omnara currently supports public linux/amd64 images for Modal pools.',
    descriptionHref: 'https://modal.com/docs/guide/existing-images',
  },
  location: {
    key: 'region',
    label: 'Region',
    placeholder: 'us-east',
    defaultValue: '',
    required: false,
  },
  scope: {
    key: 'app',
    label: 'App',
    placeholder: 'omnara',
    required: false,
    description: 'Defaults to omnara; created automatically on first provisioning.',
  },
  credential: {
    label: 'Modal credentials',
    placeholder: 'Search secrets for your Modal credentials…',
    emptyDescription: 'No secrets yet — use New secret to store your Modal credentials.',
    defaultSecretName: 'modal-api-credentials',
    secretValuePlaceholder: '{"token_id":"ak-...","token_secret":"as-..."}',
  },
  resources: { cpu: 'configured', memoryMb: 'configured' },
}

export const machinePoolProviderDefinitions = {
  unikraft,
  blaxel,
  daytona,
  freestyle,
  modal,
} satisfies Record<MachinePoolProvider, MachinePoolProviderDefinition>

export function isMachinePoolProvider(value: string): value is MachinePoolProvider {
  return Object.hasOwn(machinePoolProviderDefinitions, value)
}
