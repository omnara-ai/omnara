import { MachinePoolInputField } from './MachinePoolInputField'
import { type MachinePoolProvider, machinePoolProviderDefinitions } from './machinePoolProviders'
import { MachinePoolSizeSelect } from './MachinePoolSizeSelect'

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
        {definition.sizes && (
          <MachinePoolSizeSelect
            provider={provider}
            drafts={{ cpu, memoryGb }}
            onChange={(drafts) => {
              onCpuChange(drafts.cpu)
              onMemoryGbChange(drafts.memoryGb)
            }}
          />
        )}
        {!definition.sizes && definition.resources.cpu !== 'unsupported' && (
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
        {!definition.sizes && definition.resources.memoryMb !== 'unsupported' && (
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
