import { MachinePoolInputField } from './MachinePoolInputField'
import { type MachinePoolProvider, machinePoolProviderDefinitions } from './machinePoolProviders'

export function MachinePoolResourceFields({
  provider,
  clusterManaged,
  location,
  cpu,
  memoryGb,
  maxMachines,
  onLocationChange,
  onCpuChange,
  onMemoryGbChange,
  onMaxMachinesChange,
}: {
  provider: MachinePoolProvider
  clusterManaged: boolean
  location: string
  cpu: string
  memoryGb: string
  maxMachines: string
  onLocationChange: (value: string) => void
  onCpuChange: (value: string) => void
  onMemoryGbChange: (value: string) => void
  onMaxMachinesChange: (value: string) => void
}) {
  const definition = machinePoolProviderDefinitions[provider]
  const bounds = definition.resourceBounds
  return (
    <>
      <div className="grid gap-4 sm:grid-cols-2">
        {!clusterManaged && definition.location && (
          <MachinePoolInputField
            id="mpool-location"
            label={definition.location.label}
            required={definition.location.required}
            value={location}
            placeholder={definition.location.placeholder}
            autoComplete="off"
            onValueChange={onLocationChange}
          />
        )}
        {definition.resources.cpu !== 'unsupported' && (
          <MachinePoolInputField
            id="mpool-cpu"
            label={
              definition.resources.cpu === 'provider-resolved'
                ? 'Max vCPU / machine'
                : 'vCPU / machine'
            }
            type="number"
            min={bounds?.cpu.min ?? 1}
            max={bounds?.cpu.max}
            step="1"
            required
            value={cpu}
            onValueChange={onCpuChange}
          />
        )}
        {definition.resources.memoryMb !== 'unsupported' && (
          <MachinePoolInputField
            id="mpool-memory"
            label={
              definition.resources.memoryMb === 'provider-resolved'
                ? 'Max memory (GB) per machine'
                : 'Memory (GB) per machine'
            }
            type="number"
            min={bounds ? bounds.memoryMb.min / 1024 : 0}
            max={bounds ? bounds.memoryMb.max / 1024 : undefined}
            step="any"
            required
            description={
              bounds
                ? `${bounds.memoryMb.min / 1024}–${bounds.memoryMb.max / 1024} GB, in ${bounds.memoryMb.step} MiB increments.`
                : undefined
            }
            value={memoryGb}
            onValueChange={onMemoryGbChange}
          />
        )}
      </div>
      {!clusterManaged && (
        <MachinePoolInputField
          id="mpool-max"
          label="Max pool machines"
          type="number"
          min="0"
          step="1"
          required
          value={maxMachines}
          onValueChange={onMaxMachinesChange}
        />
      )}
    </>
  )
}
