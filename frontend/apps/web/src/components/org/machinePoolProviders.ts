import type { CreateMachinePoolRequest, MachinePool } from '@omnara/sdk'

import { providerOptionStrings } from '@/lib/provider-options'

interface MachinePoolProviderDefinition {
  label: string
  resource: {
    optional?: boolean
    key: string
    label: string
    placeholder: string
    description?: string
    descriptionHref?: string
  }
  location?: {
    key: string
    label: string
    placeholder: string
    defaultValue: string
    required: boolean
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
  const options: Record<string, string> = {}
  if (!definition.resource.optional || resource.trim() !== '') {
    options[definition.resource.key] = resource.trim()
  }
  if (definition.location && (definition.location.required || location.trim() !== '')) {
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

const freestyle: MachinePoolProviderDefinition = {
  label: 'Freestyle',
  resource: {
    key: 'snapshot',
    label: 'Snapshot',
    placeholder: 'freestyle/ubuntu-sm',
    description: 'The configured vCPU and memory must be at least the snapshot size.',
    descriptionHref: 'https://www.freestyle.sh/docs/vms/base-snapshots',
  },
  resources: {
    cpu: 'configured',
    memoryMb: 'configured',
    defaultCpu: '2',
    defaultMemoryGb: '4',
  },
}

const tenki: MachinePoolProviderDefinition = {
  label: 'Tenki',
  resource: {
    key: 'image',
    label: 'Image',
    placeholder: 'Tenki base image',
    optional: true,
    description:
      'Leave empty to use the Tenki base image, or enter a Tenki registry image reference.',
    descriptionHref: 'https://tenki.cloud/docs/sandbox/templates',
  },
  resources: { cpu: 'configured', memoryMb: 'configured' },
}

const arker: MachinePoolProviderDefinition = {
  label: 'Arker',
  resource: {
    key: 'source',
    label: 'Source VM',
    placeholder: 'ubuntu-base',
    description: 'Each machine is a fork of this VM, including its disk and running processes.',
    descriptionHref: 'https://arker.ai/docs/vms',
  },
  location: {
    key: 'region',
    label: 'Region',
    placeholder: 'aws-us-west-2',
    defaultValue: 'aws-us-west-2',
    required: true,
  },
  resources: { cpu: 'configured', memoryMb: 'configured' },
}

const boxd: MachinePoolProviderDefinition = {
  label: 'boxd',
  resource: {
    key: 'snapshot',
    label: 'Snapshot',
    placeholder: 'boxd base image',
    optional: true,
    description:
      "Leave empty to use the boxd base image, or enter a saved snapshot's name. Machines take the snapshot's size, or your boxd org's default size without one.",
    descriptionHref: 'https://docs.boxd.sh/guides/snapshots',
  },
  resources: {
    cpu: 'provider-resolved',
    memoryMb: 'provider-resolved',
    defaultCpu: '2',
    defaultMemoryGb: '8',
  },
}

const createos: MachinePoolProviderDefinition = {
  label: 'CreateOS',
  resource: {
    key: 'shape',
    label: 'Shape',
    placeholder: 's-2vcpu-4gb',
    description: 'The max vCPU and memory must be at least the shape size.',
    descriptionHref: 'https://docs.createos.sh/Sandbox/Limits',
  },
  location: {
    key: 'rootfs',
    label: 'Root filesystem',
    placeholder: 'devbox:1',
    defaultValue: 'devbox:1',
    required: true,
  },
  resources: {
    cpu: 'provider-resolved',
    memoryMb: 'provider-resolved',
    defaultCpu: '2',
    defaultMemoryGb: '4',
  },
}

export const machinePoolProviderDefinitions = {
  unikraft,
  blaxel,
  daytona,
  modal,
  freestyle,
  tenki,
  arker,
  boxd,
  createos,
} satisfies Record<MachinePoolProvider, MachinePoolProviderDefinition>

export function isMachinePoolProvider(value: string): value is MachinePoolProvider {
  return Object.hasOwn(machinePoolProviderDefinitions, value)
}
