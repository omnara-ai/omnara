import type {
  ConfiguredModel,
  MachinePool,
  ProjectMachinePoolGrant,
  ProjectModelGrant,
} from '@omnara/sdk'

import { formatMemoryGb } from '@/lib/machine-memory'

/** One setting a project grant changes, with the org default it replaces when known. */
export interface GrantOverride {
  label: string
  value: string
  /** The org-level value; undefined while the org resource isn't loaded or isn't visible. */
  inherited?: string
}

function tokens(value: number | null | undefined) {
  return value == null ? 'Not set' : `${value.toLocaleString()} tokens`
}

function toggle(value: boolean | null | undefined) {
  if (value == null) return 'Not set'
  return value ? 'Supported' : 'Not supported'
}

function list(values: readonly string[]) {
  return values.length === 0 ? 'None' : values.join(', ')
}

/** Model settings a project grant overrides. Null, empty string, and empty list all inherit. */
export function modelGrantOverrides(
  grant: ProjectModelGrant,
  model: ConfiguredModel | undefined,
): GrantOverride[] {
  const overrides: GrantOverride[] = []
  const add = (label: string, value: string, inherited: string | undefined) => {
    overrides.push({ label, value, inherited })
  }
  if (grant.context_window_tokens != null) {
    add(
      'Context window',
      tokens(grant.context_window_tokens),
      model && tokens(model.context_window_tokens),
    )
  }
  if (grant.max_output_tokens != null) {
    add('Max output', tokens(grant.max_output_tokens), model && tokens(model.max_output_tokens))
  }
  if (grant.default_max_output_tokens != null) {
    add(
      'Default max output',
      tokens(grant.default_max_output_tokens),
      model && tokens(model.default_max_output_tokens),
    )
  }
  if (grant.default_cache_retention != null) {
    add('Cache retention', grant.default_cache_retention, model?.default_cache_retention)
  }
  if (grant.supports_tools != null) {
    add('Tools', toggle(grant.supports_tools), model && toggle(model.supports_tools))
  }
  if (grant.supports_reasoning != null) {
    add('Reasoning', toggle(grant.supports_reasoning), model && toggle(model.supports_reasoning))
  }
  if (grant.default_reasoning_effort) {
    add('Reasoning effort', grant.default_reasoning_effort, model?.default_reasoning_effort)
  }
  if (grant.supported_reasoning_efforts.length > 0) {
    add(
      'Reasoning efforts',
      list(grant.supported_reasoning_efforts),
      model && list(model.supported_reasoning_efforts),
    )
  }
  if (grant.input_modalities.length > 0) {
    add('Input modalities', list(grant.input_modalities), model && list(model.input_modalities))
  }
  if (grant.output_modalities.length > 0) {
    add('Output modalities', list(grant.output_modalities), model && list(model.output_modalities))
  }
  return overrides
}

function cpu(value: number | null) {
  return value === null ? 'No limit' : `${value} vCPU`
}

function memory(value: number | null) {
  return formatMemoryGb(value) ?? 'No limit'
}

function idleDeletion(minutes: number | null) {
  if (!minutes) return 'Off'
  return minutes === 1 ? '1 minute' : `${minutes} minutes`
}

function variables(count: number) {
  return count === 1 ? '1 variable' : `${count} variables`
}

/** Pool settings a project grant overrides. Null, empty string, and empty maps all inherit. */
export function poolGrantOverrides(
  grant: ProjectMachinePoolGrant,
  pool: MachinePool | undefined,
): GrantOverride[] {
  const overrides: GrantOverride[] = []
  const add = (label: string, value: string, inherited: string | undefined) => {
    overrides.push({ label, value, inherited })
  }
  if (grant.max_total_machines !== null) {
    add('Machine quota', String(grant.max_total_machines), pool && String(pool.max_total_machines))
  }
  if (grant.max_total_cpu !== null) {
    add('CPU quota', cpu(grant.max_total_cpu), pool && cpu(pool.max_total_cpu))
  }
  if (grant.max_total_memory_mb !== null) {
    add('Memory quota', memory(grant.max_total_memory_mb), pool && memory(pool.max_total_memory_mb))
  }
  if (grant.default_machine_cpu !== null) {
    add(
      'Default CPU per machine',
      cpu(grant.default_machine_cpu),
      pool && cpu(pool.default_machine_cpu),
    )
  }
  if (grant.default_machine_memory_mb !== null) {
    add(
      'Default memory per machine',
      memory(grant.default_machine_memory_mb),
      pool && memory(pool.default_machine_memory_mb),
    )
  }
  if (grant.min_machine_cpu !== null) {
    add('Min CPU per machine', cpu(grant.min_machine_cpu), pool && cpu(pool.min_machine_cpu))
  }
  if (grant.max_machine_cpu !== null) {
    add('Max CPU per machine', cpu(grant.max_machine_cpu), pool && cpu(pool.max_machine_cpu))
  }
  if (grant.min_machine_memory_mb !== null) {
    add(
      'Min memory per machine',
      memory(grant.min_machine_memory_mb),
      pool && memory(pool.min_machine_memory_mb),
    )
  }
  if (grant.max_machine_memory_mb !== null) {
    add(
      'Max memory per machine',
      memory(grant.max_machine_memory_mb),
      pool && memory(pool.max_machine_memory_mb),
    )
  }
  if (grant.delete_after_idle_minutes !== null) {
    add(
      'Idle deletion',
      idleDeletion(grant.delete_after_idle_minutes),
      pool && idleDeletion(pool.delete_after_idle_minutes),
    )
  }
  if (grant.default_cwd) {
    add('Working directory', grant.default_cwd, pool && (pool.default_cwd || 'Default'))
  }
  if (Object.keys(grant.default_machine_env_overlay).length > 0) {
    add(
      'Environment',
      `${variables(Object.keys(grant.default_machine_env_overlay).length)} changed`,
      pool && variables(Object.keys(pool.default_machine_env).length),
    )
  }
  if (Object.keys(grant.default_machine_secret_env_overlay).length > 0) {
    add(
      'Secret environment',
      `${variables(Object.keys(grant.default_machine_secret_env_overlay).length)} changed`,
      pool && variables(Object.keys(pool.default_machine_secret_env).length),
    )
  }
  const providerOptionKeys = Object.keys(grant.default_machine_provider_options_overlay)
  if (providerOptionKeys.length > 0) {
    add('Provider options', providerOptionKeys.join(', '), undefined)
  }
  return overrides
}
