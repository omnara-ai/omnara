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
    /** The pool may leave the resource empty to boot the provider's base image. */
    optional?: boolean
  }
  /** Omitted for providers without a region-like placement setting. */
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

const boxd: MachinePoolProviderDefinition = {
  label: 'boxd',
  resource: {
    key: 'snapshot',
    label: 'Snapshot (optional)',
    placeholder: 'Leave empty for the boxd base image',
    description:
      'Machines boot the boxd base image unless a snapshot saved with `boxd snapshots save` is named. Sizes must be 1 vCPU with 4 GB, 2 with 8 GB, or 4 with 16 GB.',
    descriptionHref: 'https://docs.boxd.sh/guides/snapshots',
    optional: true,
  },
  credential: {
    label: 'boxd API key',
    placeholder: 'Search secrets for your boxd API key…',
    emptyDescription: 'No secrets yet — use New secret to store your boxd API key.',
    defaultSecretName: 'boxd-api-key',
    secretValuePlaceholder: 'bxd_...',
  },
  resources: { cpu: 'configured', memoryMb: 'configured' },
}

export const machinePoolProviderDefinitions = {
  unikraft,
  blaxel,
  daytona,
  modal,
  boxd,
} satisfies Record<MachinePoolProvider, MachinePoolProviderDefinition>

export function isMachinePoolProvider(value: string): value is MachinePoolProvider {
  return Object.hasOwn(machinePoolProviderDefinitions, value)
}
