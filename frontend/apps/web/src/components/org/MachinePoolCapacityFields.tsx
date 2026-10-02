import { Minus, Plus } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Field, RequiredFieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

import { machinePoolMachineSizeLabel, machinePoolTotalLabel } from './machinePoolCreateSteps'
import type { MachinePoolFormValues } from './MachinePoolDialogState'
import type { MachinePoolFormSetValue } from './MachinePoolFields'
import { MachinePoolInputField } from './MachinePoolInputField'
import { machinePoolProviderDefinitions } from './machinePoolProviders'

const maxPreviewMachines = 8

/** Location, per-machine size, and machine count, with a preview of the machines they allow. */
export function MachinePoolCapacityFields({
  values,
  setValue,
}: {
  values: MachinePoolFormValues
  setValue: MachinePoolFormSetValue
}) {
  const definition = machinePoolProviderDefinitions[values.provider]
  const { cpu, memoryMb } = definition.resources
  const sizeFields = [cpu !== 'unsupported', memoryMb !== 'unsupported', true].filter(Boolean)
  return (
    <>
      {definition.location && (
        <MachinePoolInputField
          id="mpool-location"
          label={definition.location.label}
          required={definition.location.required}
          value={values.location}
          placeholder={definition.location.placeholder}
          autoComplete="off"
          onValueChange={(location) => {
            setValue('location', location)
          }}
        />
      )}
      <div
        className={
          sizeFields.length === 3 ? 'grid gap-4 sm:grid-cols-3' : 'grid gap-4 sm:grid-cols-2'
        }
      >
        {cpu !== 'unsupported' && (
          <UnitInputField
            id="mpool-cpu"
            label={cpu === 'provider-resolved' ? 'Max vCPU per machine' : 'vCPU per machine'}
            unit="vCPU"
            min="1"
            step="1"
            value={values.cpu}
            onValueChange={(value) => {
              setValue('cpu', value)
            }}
          />
        )}
        {memoryMb !== 'unsupported' && (
          <UnitInputField
            id="mpool-memory"
            label={
              memoryMb === 'provider-resolved' ? 'Max memory per machine' : 'Memory per machine'
            }
            unit="GB"
            min="0"
            step="any"
            value={values.memoryGb}
            onValueChange={(value) => {
              setValue('memoryGb', value)
            }}
          />
        )}
        <MaxMachinesField
          value={values.maxMachines}
          onValueChange={(value) => {
            setValue('maxMachines', value)
          }}
        />
      </div>
      <CapacityPreview values={values} />
    </>
  )
}

function UnitInputField({
  id,
  label,
  unit,
  value,
  onValueChange,
  ...inputProps
}: {
  id: string
  label: string
  unit: string
  value: string
  min: string
  step: string
  onValueChange: (value: string) => void
}) {
  return (
    <Field>
      <RequiredFieldLabel htmlFor={id}>{label}</RequiredFieldLabel>
      <div className="relative">
        <Input
          {...inputProps}
          id={id}
          type="number"
          required
          value={value}
          className="pr-14 [appearance:textfield] [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none"
          onChange={(event) => {
            onValueChange(event.target.value)
          }}
        />
        <span
          aria-hidden="true"
          className="text-muted-foreground pointer-events-none absolute inset-y-0 right-3 flex items-center text-sm"
        >
          {unit}
        </span>
      </div>
    </Field>
  )
}

function MaxMachinesField({
  value,
  onValueChange,
}: {
  value: string
  onValueChange: (value: string) => void
}) {
  const current = Number(value)
  const count = Number.isInteger(current) && current >= 0 ? current : undefined
  return (
    <Field>
      <RequiredFieldLabel htmlFor="mpool-max">Max machines</RequiredFieldLabel>
      <div className="relative">
        <Input
          id="mpool-max"
          type="number"
          min="0"
          step="1"
          required
          value={value}
          className="pr-20 [appearance:textfield] [&::-webkit-inner-spin-button]:appearance-none [&::-webkit-outer-spin-button]:appearance-none"
          onChange={(event) => {
            onValueChange(event.target.value)
          }}
        />
        <div className="absolute inset-y-0 right-1 flex items-center">
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="size-8"
            aria-label="Fewer machines"
            disabled={count === undefined || count === 0}
            onClick={() => {
              if (count !== undefined) onValueChange(String(Math.max(0, count - 1)))
            }}
          >
            <Minus className="size-3.5" />
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className="size-8"
            aria-label="More machines"
            onClick={() => {
              onValueChange(String((count ?? 0) + 1))
            }}
          >
            <Plus className="size-3.5" />
          </Button>
        </div>
      </div>
    </Field>
  )
}

function CapacityPreview({ values }: { values: MachinePoolFormValues }) {
  const count = Number(values.maxMachines)
  const size = machinePoolMachineSizeLabel(values)
  if (!Number.isInteger(count) || count < 0 || values.maxMachines.trim() === '' || !size) {
    return null
  }
  const shown = Math.min(count, maxPreviewMachines)
  const hidden = count - shown
  const location = values.location.trim()
  const total = machinePoolTotalLabel(values)
  return (
    <div className="bg-muted/40 flex flex-col gap-3 rounded-lg border p-4">
      {count === 0 ? (
        <p className="text-muted-foreground text-sm">
          The pool is paused: no machines start until max machines is above 0.
        </p>
      ) : (
        <>
          <ul
            aria-label="Machine preview"
            className="grid gap-2"
            style={{ gridTemplateColumns: `repeat(${Math.min(shown, 4)}, minmax(0, 1fr))` }}
          >
            {Array.from({ length: shown }, (_, index) => (
              <li
                key={index}
                className="border-primary/40 bg-primary/10 text-primary truncate rounded-md border px-2 py-3 text-center font-mono text-xs"
              >
                {index === shown - 1 && hidden > 0 ? `+${hidden + 1} more` : size}
              </li>
            ))}
          </ul>
          <p className="text-muted-foreground text-sm">
            Up to {count} {count === 1 ? 'machine' : 'machines'}
            {location && ` in ${location}`}
            {total && ` · ${total}`}
          </p>
        </>
      )}
    </div>
  )
}
