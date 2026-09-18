import type { BasicMachineSource } from '@/components/agents/useAgentBuilderForm'
import { ProviderOptionsOverrideFields } from '@/components/machines/MachineOverrideFields'
import {
  isMachinePoolProvider,
  machinePoolProviderDefinitions,
} from '@/components/org/machinePoolProviders'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'

export function SourceOverridesSection({
  source,
  onChange,
}: {
  source: BasicMachineSource
  onChange: (patch: Partial<BasicMachineSource>) => void
}) {
  const provider =
    source.kind === 'pool' && isMachinePoolProvider(source.provider) ? source.provider : null
  return (
    <FieldGroup>
      {source.kind === 'pool' && (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field>
            <FieldLabel htmlFor={`${source.id}-initial`}>Initial machines</FieldLabel>
            <Input
              id={`${source.id}-initial`}
              type="number"
              min={0}
              value={source.initialNumMachines}
              placeholder="1"
              onChange={(event) => {
                onChange({ initialNumMachines: event.target.value })
              }}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor={`${source.id}-max`}>Max machines</FieldLabel>
            <Input
              id={`${source.id}-max`}
              type="number"
              min={0}
              value={source.maxMachines}
              placeholder="1"
              onChange={(event) => {
                onChange({ maxMachines: event.target.value })
              }}
            />
          </Field>
        </div>
      )}
      {provider && (
        <ProviderOptionsOverrideFields
          idPrefix={source.id}
          pool={{ provider: source.provider, management_kind: source.managementKind }}
          values={source.providerOptions}
          onChange={(providerOptions) => {
            onChange({ providerOptions })
          }}
        />
      )}
      {source.kind === 'pool' && (
        <Field>
          <FieldLabel htmlFor={`${source.id}-delete-after-idle`}>
            Delete after idle minutes
          </FieldLabel>
          <Input
            id={`${source.id}-delete-after-idle`}
            type="number"
            min={0}
            step={1}
            value={source.deleteAfterIdleMinutes}
            onChange={(event) => {
              onChange({ deleteAfterIdleMinutes: event.target.value })
            }}
          />
        </Field>
      )}
    </FieldGroup>
  )
}

export function SourceResourceFields({
  source,
  onChange,
}: {
  source: BasicMachineSource
  onChange: (patch: Partial<BasicMachineSource>) => void
}) {
  const provider =
    source.kind === 'pool' && isMachinePoolProvider(source.provider) ? source.provider : null
  const resources = provider ? machinePoolProviderDefinitions[provider].resources : null
  if (resources?.cpu !== 'configured' && resources?.memoryMb !== 'configured') return null
  const both = resources.cpu === 'configured' && resources.memoryMb === 'configured'
  return (
    <div className={cn('grid gap-4', both && 'sm:grid-cols-2')}>
      {resources.memoryMb === 'configured' && (
        <Field>
          <FieldLabel htmlFor={`${source.id}-memory`}>Machine memory (GB)</FieldLabel>
          <Input
            id={`${source.id}-memory`}
            type="number"
            min={0}
            step="any"
            value={source.machineMemoryGb}
            onChange={(event) => {
              onChange({ machineMemoryGb: event.target.value })
            }}
          />
        </Field>
      )}
      {resources.cpu === 'configured' && (
        <Field>
          <FieldLabel htmlFor={`${source.id}-cpu`}>Machine CPU</FieldLabel>
          <Input
            id={`${source.id}-cpu`}
            type="number"
            min={1}
            value={source.machineCpu}
            onChange={(event) => {
              onChange({ machineCpu: event.target.value })
            }}
          />
        </Field>
      )}
    </div>
  )
}
