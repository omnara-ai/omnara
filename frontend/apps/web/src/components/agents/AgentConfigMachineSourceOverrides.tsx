import type { BasicMachineSource } from '@/components/agents/agentConfigBasicSerialization'
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

export function SourceOverridesSection({
  orgId,
  projectId,
  source,
  onChange,
}: {
  orgId: string
  projectId: string
  source: BasicMachineSource
  onChange: (patch: Partial<BasicMachineSource>) => void
}) {
  const provider =
    source.kind === 'pool' && isMachinePoolProvider(source.provider) ? source.provider : null
  const resources = provider ? machinePoolProviderDefinitions[provider].resources : null
  return (
    <OverridesCollapsible>
      <FieldGroup>
        {provider && (
          <>
            <ProviderOptionsOverrideFields
              idPrefix={source.id}
              pool={{ provider: source.provider, management_kind: source.managementKind }}
              values={source.providerOptions}
              onChange={(providerOptions) => {
                onChange({ providerOptions })
              }}
            />
            {(resources?.cpu === 'configured' || resources?.memoryMb === 'configured') && (
              <div className="grid gap-4 sm:grid-cols-2">
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
                {resources.memoryMb === 'configured' && (
                  <Field>
                    <FieldLabel htmlFor={`${source.id}-memory`}>Machine memory (MB)</FieldLabel>
                    <Input
                      id={`${source.id}-memory`}
                      type="number"
                      min={1}
                      value={source.machineMemoryMb}
                      onChange={(event) => {
                        onChange({ machineMemoryMb: event.target.value })
                      }}
                    />
                  </Field>
                )}
              </div>
            )}
          </>
        )}
        {source.kind === 'pool' && !provider && (
          <FieldDescription>
            Select a machine pool to configure provider overrides.
          </FieldDescription>
        )}
        <EnvOverlayEditor
          label="Environment overlay"
          description="Overrides environment variables on this agent's machines."
          rows={source.envRows}
          onRowsChange={(envRows) => {
            onChange({ envRows })
          }}
        />
        <SecretEnvOverlayEditor
          orgId={orgId}
          projectId={projectId}
          enabled
          label="Secret environment overlay"
          description="Overrides secret environment variables on this agent's machines."
          rows={source.secretEnvRows}
          onRowsChange={(secretEnvRows) => {
            onChange({ secretEnvRows })
          }}
        />
      </FieldGroup>
    </OverridesCollapsible>
  )
}
