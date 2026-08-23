import {
  CombinedEnvOverlayEditor,
  OverridesCollapsible,
} from '@/components/machines/MachineOverrideFields'
import { CheckboxField, FieldGroup } from '@/components/ui/field'

import {
  derivedMemoryTotalCapPlaceholder,
  derivedTotalCapPlaceholder,
  type MachinePoolFormValues,
} from './MachinePoolDialogState'
import { MachinePoolInputField } from './MachinePoolInputField'
import { machinePoolProviderDefinitions } from './machinePoolProviders'

export function MachinePoolAdvancedSection({
  orgId,
  enabled,
  clusterManaged,
  values,
  setValue,
}: {
  orgId: string
  enabled: boolean
  clusterManaged: boolean
  values: MachinePoolFormValues
  setValue: <K extends keyof MachinePoolFormValues>(key: K, value: MachinePoolFormValues[K]) => void
}) {
  const resources = machinePoolProviderDefinitions[values.provider].resources
  return (
    <OverridesCollapsible title="Advanced">
      <FieldGroup>
        {!clusterManaged && (
          <CheckboxField
            label="Runtime protection"
            description="Delete a sandbox if its provider remains running after its Omnara daemon becomes inactive."
            inputClassName="self-start"
            checked={values.runtimeProtectionEnabled}
            onChange={(event) => {
              setValue('runtimeProtectionEnabled', event.target.checked)
            }}
          />
        )}
        <MachinePoolInputField
          id="mpool-delete-after-idle"
          label="Delete after idle minutes"
          type="number"
          min="5"
          step="1"
          description="Leave empty for no pool default; values must be at least 5."
          value={values.deleteAfterIdleMinutes}
          onValueChange={(value) => {
            setValue('deleteAfterIdleMinutes', value)
          }}
        />
        {!clusterManaged && (
          <MachinePoolInputField
            id="mpool-cwd"
            label="Working directory"
            value={values.cwd}
            placeholder="/workspace"
            onValueChange={(value) => {
              setValue('cwd', value)
            }}
          />
        )}
        <CombinedEnvOverlayEditor
          orgId={orgId}
          enabled={enabled}
          envRows={values.envRows}
          secretEnvRows={values.secretEnvRows}
          onChange={({ envRows, secretEnvRows }) => {
            setValue('envRows', envRows)
            setValue('secretEnvRows', secretEnvRows)
          }}
        />
        <div className="grid gap-4 sm:grid-cols-3">
          {resources.cpu !== 'unsupported' && (
            <>
              {!clusterManaged && (
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
              )}
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
              {resources.cpu === 'configured' && (
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
              )}
            </>
          )}
          {resources.memoryMb !== 'unsupported' && (
            <>
              {!clusterManaged && (
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
              )}
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
              {resources.memoryMb === 'configured' && (
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
              )}
            </>
          )}
        </div>
      </FieldGroup>
    </OverridesCollapsible>
  )
}
