import { type MachinePool } from '@omnara/sdk'

import {
  EnvOverlayEditor,
  OverridesCollapsible,
  ProviderOptionsOverrideFields,
  SecretEnvOverlayEditor,
} from '@/components/machines/MachineOverrideFields'
import {
  isMachinePoolProvider,
  machinePoolProviderDefinitions,
} from '@/components/org/machinePoolProviders'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { type PoolGrantOverrideDraft } from './GrantMachinePoolDialogState'

export function PoolGrantOverridesCollapsible({
  orgId,
  projectId,
  enabled,
  idPrefix,
  pool,
  values,
  onChange,
}: {
  orgId: string
  /** Target project when known; scopes secret selection to project-available secrets. */
  projectId?: string
  enabled: boolean
  idPrefix: string
  pool: MachinePool
  values: PoolGrantOverrideDraft
  onChange: (values: PoolGrantOverrideDraft) => void
}) {
  return (
    <OverridesCollapsible>
      <FieldGroup>
        <FieldDescription>
          Pool values are shown as placeholders. Fields left empty keep following the pool; values
          you enter are stored as overrides for this grant.
        </FieldDescription>
        <PoolGrantOverrideFields
          orgId={orgId}
          projectId={projectId}
          enabled={enabled}
          idPrefix={idPrefix}
          pool={pool}
          values={values}
          onChange={onChange}
        />
      </FieldGroup>
    </OverridesCollapsible>
  )
}

function NumberField({
  id,
  label,
  value,
  placeholder,
  onValueChange,
}: {
  id: string
  label: string
  value: string
  placeholder?: string
  onValueChange: (value: string) => void
}) {
  return (
    <Field>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      <Input
        id={id}
        type="number"
        min={0}
        value={value}
        placeholder={placeholder}
        onChange={(event) => {
          onValueChange(event.target.value)
        }}
      />
    </Field>
  )
}

function numberPlaceholder(value: number | null) {
  return value === null ? undefined : String(value)
}

export function PoolGrantOverrideFields({
  orgId,
  projectId,
  enabled,
  idPrefix,
  pool,
  values,
  onChange,
}: {
  orgId: string
  projectId?: string
  enabled: boolean
  idPrefix: string
  pool: MachinePool
  values: PoolGrantOverrideDraft
  onChange: (values: PoolGrantOverrideDraft) => void
}) {
  const provider = isMachinePoolProvider(pool.provider) ? pool.provider : null
  const resources = provider ? machinePoolProviderDefinitions[provider].resources : null
  return (
    <FieldGroup>
      <ProviderOptionsOverrideFields
        idPrefix={idPrefix}
        pool={pool}
        defaults={pool.default_machine_provider_options}
        values={values.providerOptions}
        onChange={(providerOptions) => {
          onChange({ ...values, providerOptions })
        }}
      />
      <div className="grid gap-4 sm:grid-cols-3">
        {resources?.cpu === 'configured' && (
          <NumberField
            id={`${idPrefix}-cpu`}
            label="Machine CPU"
            value={values.cpu}
            placeholder={numberPlaceholder(pool.default_machine_cpu)}
            onValueChange={(cpu) => {
              onChange({ ...values, cpu })
            }}
          />
        )}
        {resources?.memoryMb === 'configured' && (
          <NumberField
            id={`${idPrefix}-memory`}
            label="Machine memory (MB)"
            value={values.memoryMb}
            placeholder={numberPlaceholder(pool.default_machine_memory_mb)}
            onValueChange={(memoryMb) => {
              onChange({ ...values, memoryMb })
            }}
          />
        )}
        <Field>
          <FieldLabel htmlFor={`${idPrefix}-cwd`}>Working directory</FieldLabel>
          <Input
            id={`${idPrefix}-cwd`}
            value={values.cwd}
            placeholder={pool.default_cwd || '/workspace'}
            onChange={(event) => {
              onChange({ ...values, cwd: event.target.value })
            }}
          />
        </Field>
      </div>
      <EnvOverlayEditor
        label="Environment variables"
        description="Set on top of the pool's variables for machines from this grant."
        rows={values.envRows}
        onRowsChange={(envRows) => {
          onChange({ ...values, envRows })
        }}
      />
      <SecretEnvOverlayEditor
        orgId={orgId}
        projectId={projectId}
        enabled={enabled}
        label="Secret environment variables"
        description="Set on top of the pool's secret variables for machines from this grant."
        rows={values.secretEnvRows}
        onRowsChange={(secretEnvRows) => {
          onChange({ ...values, secretEnvRows })
        }}
      />
      <Field>
        <FieldLabel>Limits</FieldLabel>
        <div className="grid gap-4 sm:grid-cols-3">
          <NumberField
            id={`${idPrefix}-max-machines`}
            label="Max machines"
            value={values.maxTotalMachines}
            placeholder={String(pool.max_total_machines)}
            onValueChange={(maxTotalMachines) => {
              onChange({ ...values, maxTotalMachines })
            }}
          />
          {resources?.cpu !== 'unsupported' && (
            <>
              <NumberField
                id={`${idPrefix}-max-total-cpu`}
                label="Max total CPU"
                value={values.maxTotalCpu}
                placeholder={numberPlaceholder(pool.max_total_cpu)}
                onValueChange={(maxTotalCpu) => {
                  onChange({ ...values, maxTotalCpu })
                }}
              />
              <NumberField
                id={`${idPrefix}-min-machine-cpu`}
                label="Min machine CPU"
                value={values.minMachineCpu}
                placeholder={numberPlaceholder(pool.min_machine_cpu)}
                onValueChange={(minMachineCpu) => {
                  onChange({ ...values, minMachineCpu })
                }}
              />
              <NumberField
                id={`${idPrefix}-max-machine-cpu`}
                label="Max machine CPU"
                value={values.maxMachineCpu}
                placeholder={numberPlaceholder(pool.max_machine_cpu)}
                onValueChange={(maxMachineCpu) => {
                  onChange({ ...values, maxMachineCpu })
                }}
              />
            </>
          )}
          {resources?.memoryMb !== 'unsupported' && (
            <>
              <NumberField
                id={`${idPrefix}-max-total-memory`}
                label="Max total memory (MB)"
                value={values.maxTotalMemoryMb}
                placeholder={numberPlaceholder(pool.max_total_memory_mb)}
                onValueChange={(maxTotalMemoryMb) => {
                  onChange({ ...values, maxTotalMemoryMb })
                }}
              />
              <NumberField
                id={`${idPrefix}-min-machine-memory`}
                label="Min machine memory (MB)"
                value={values.minMachineMemoryMb}
                placeholder={numberPlaceholder(pool.min_machine_memory_mb)}
                onValueChange={(minMachineMemoryMb) => {
                  onChange({ ...values, minMachineMemoryMb })
                }}
              />
              <NumberField
                id={`${idPrefix}-max-machine-memory`}
                label="Max machine memory (MB)"
                value={values.maxMachineMemoryMb}
                placeholder={numberPlaceholder(pool.max_machine_memory_mb)}
                onValueChange={(maxMachineMemoryMb) => {
                  onChange({ ...values, maxMachineMemoryMb })
                }}
              />
            </>
          )}
        </div>
      </Field>
    </FieldGroup>
  )
}
