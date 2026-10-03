import { useState } from 'react'

import { OverridesCollapsible } from '@/components/machines/MachineOverrideFields'
import { CheckboxField, FieldGroup } from '@/components/ui/field'

import {
  derivedMemoryTotalCapPlaceholder,
  derivedTotalCapPlaceholder,
  type MachinePoolFormSetValue,
  type MachinePoolFormValues,
} from './MachinePoolDialogState'
import { MachinePoolInputField } from './MachinePoolInputField'
import { machinePoolProviderDefinitions } from './machinePoolProviders'
import { type MachinePoolFieldErrors, type MachinePoolFieldKey } from './machinePoolValidation'

const advancedFieldKeys = [
  'deleteAfterIdleMinutes',
  'maxTotalCpu',
  'minMachineCpu',
  'maxMachineCpu',
  'maxTotalMemoryGb',
  'minMachineMemoryGb',
  'maxMachineMemoryGb',
] as const satisfies readonly MachinePoolFieldKey[]

/**
 * Resource caps and machine lifecycle, collapsed by default. It stays open while one of its
 * fields is invalid so the error that blocks the form is always visible.
 */
export function MachinePoolAdvancedSection({
  clusterManaged,
  values,
  setValue,
  errors,
}: {
  clusterManaged: boolean
  values: MachinePoolFormValues
  setValue: MachinePoolFormSetValue
  errors: MachinePoolFieldErrors
}) {
  const invalid = advancedFieldKeys.some((key) => errors[key] !== undefined)
  const [open, setOpen] = useState(invalid)
  const resources = machinePoolProviderDefinitions[values.provider].resources
  return (
    <OverridesCollapsible
      title="Advanced"
      open={open || invalid}
      onOpenChange={(nextOpen) => {
        if (nextOpen || !invalid) setOpen(nextOpen)
      }}
    >
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
          error={errors.deleteAfterIdleMinutes}
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
                  error={errors.maxTotalCpu}
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
                error={errors.minMachineCpu}
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
                  error={errors.maxMachineCpu}
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
                  error={errors.maxTotalMemoryGb}
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
                error={errors.minMachineMemoryGb}
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
                  error={errors.maxMachineMemoryGb}
                />
              )}
            </>
          )}
        </div>
      </FieldGroup>
    </OverridesCollapsible>
  )
}
