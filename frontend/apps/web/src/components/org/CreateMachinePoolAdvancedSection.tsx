import {
  EnvOverlayEditor,
  OverridesCollapsible,
  SecretEnvOverlayEditor,
} from '@/components/machines/MachineOverrideFields'
import {
  CheckboxField,
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'

import {
  derivedMemoryTotalCapPlaceholder,
  derivedTotalCapPlaceholder,
  type MachinePoolFormValues,
} from './CreateMachinePoolDialogState'
import { MachinePoolInputField } from './MachinePoolInputField'
import { machinePoolProviderDefinitions } from './machinePoolProviders'

export function CreateMachinePoolAdvancedSection({
  orgId,
  enabled,
  values,
  setValue,
}: {
  orgId: string
  enabled: boolean
  values: MachinePoolFormValues
  setValue: <K extends keyof MachinePoolFormValues>(key: K, value: MachinePoolFormValues[K]) => void
}) {
  const resources = machinePoolProviderDefinitions[values.provider].resources
  return (
    <OverridesCollapsible title="Advanced">
      <FieldGroup>
        <CheckboxField
          label="Runtime protection"
          description="Delete a sandbox if its provider remains running after its Omnara daemon becomes inactive."
          checked={values.runtimeProtectionEnabled}
          onChange={(event) => {
            setValue('runtimeProtectionEnabled', event.target.checked)
          }}
        />
        <MachinePoolInputField
          id="mpool-cwd"
          label="Working directory"
          value={values.cwd}
          placeholder="/workspace"
          onValueChange={(value) => {
            setValue('cwd', value)
          }}
        />
        <EnvOverlayEditor
          label="Environment variables"
          description="Set on every machine this pool provisions."
          rows={values.envRows}
          onRowsChange={(envRows) => {
            setValue('envRows', envRows)
          }}
        />
        <SecretEnvOverlayEditor
          orgId={orgId}
          enabled={enabled}
          label="Secret environment variables"
          description="Resolved from organization secrets when a machine starts."
          rows={values.secretEnvRows}
          onRowsChange={(secretEnvRows) => {
            setValue('secretEnvRows', secretEnvRows)
          }}
        />
        <Field>
          <FieldLabel>Capacity limits</FieldLabel>
          <FieldDescription>
            Machine maximums default to the machine size; total caps default to machine size × max
            pool machines.
          </FieldDescription>
          <div className="grid gap-4 sm:grid-cols-3">
            {resources.cpu !== 'unsupported' && (
              <>
                <MachinePoolInputField
                  id="mpool-max-total-cpu"
                  label="Max total CPU"
                  type="number"
                  min="0"
                  step="1"
                  value={values.maxTotalCpu}
                  placeholder={derivedTotalCapPlaceholder(values.cpu, values.maxMachines)}
                  onValueChange={(value) => {
                    setValue('maxTotalCpu', value)
                  }}
                />
                <MachinePoolInputField
                  id="mpool-min-machine-cpu"
                  label="Min machine CPU"
                  type="number"
                  min="0"
                  step="1"
                  value={values.minMachineCpu}
                  placeholder="0"
                  onValueChange={(value) => {
                    setValue('minMachineCpu', value)
                  }}
                />
                <MachinePoolInputField
                  id="mpool-max-machine-cpu"
                  label="Max machine CPU"
                  type="number"
                  min="1"
                  step="1"
                  value={values.maxMachineCpu}
                  placeholder={values.cpu || undefined}
                  onValueChange={(value) => {
                    setValue('maxMachineCpu', value)
                  }}
                />
              </>
            )}
            {resources.memoryMb !== 'unsupported' && (
              <>
                <MachinePoolInputField
                  id="mpool-max-total-memory"
                  label="Max total memory (GB)"
                  type="number"
                  min="0"
                  step="any"
                  value={values.maxTotalMemoryGb}
                  placeholder={derivedMemoryTotalCapPlaceholder(
                    values.memoryGb,
                    values.maxMachines,
                  )}
                  onValueChange={(value) => {
                    setValue('maxTotalMemoryGb', value)
                  }}
                />
                <MachinePoolInputField
                  id="mpool-min-machine-memory"
                  label="Min machine memory (GB)"
                  type="number"
                  min="0"
                  step="any"
                  value={values.minMachineMemoryGb}
                  placeholder="0"
                  onValueChange={(value) => {
                    setValue('minMachineMemoryGb', value)
                  }}
                />
                <MachinePoolInputField
                  id="mpool-max-machine-memory"
                  label="Max machine memory (GB)"
                  type="number"
                  min="0"
                  step="any"
                  value={values.maxMachineMemoryGb}
                  placeholder={values.memoryGb || undefined}
                  onValueChange={(value) => {
                    setValue('maxMachineMemoryGb', value)
                  }}
                />
              </>
            )}
          </div>
        </Field>
      </FieldGroup>
    </OverridesCollapsible>
  )
}
