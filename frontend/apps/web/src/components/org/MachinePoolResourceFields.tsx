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
  const hasSizeControls =
    (definition.resources.showSizeControls ?? true) &&
    (definition.resources.cpu !== 'unsupported' || definition.resources.memoryMb !== 'unsupported')
  return (
    <>
      <div className={hasSizeControls ? 'grid gap-4 sm:grid-cols-2' : 'grid gap-4'}>
        {!clusterManaged && (
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
        {(definition.resources.showSizeControls ?? true) &&
          definition.resources.cpu !== 'unsupported' && (
          <MachinePoolInputField
            id="mpool-cpu"
            label={
              definition.resources.cpu === 'provider-resolved'
                ? 'Max vCPU / machine'
                : 'vCPU / machine'
            }
            type="number"
            min="1"
            step="1"
            required
            value={cpu}
            onValueChange={onCpuChange}
          />
        )}
        {(definition.resources.showSizeControls ?? true) &&
          definition.resources.memoryMb !== 'unsupported' && (
            <MachinePoolInputField
              id="mpool-memory"
              label={
                definition.resources.memoryMb === 'provider-resolved'
                  ? 'Max memory (GB) per machine'
                  : 'Memory (GB) per machine'
              }
              type="number"
              min="0"
              step="any"
              required
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
