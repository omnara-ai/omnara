import { StartupScriptField } from '@/components/machines/StartupScriptField'
import { CredentialSecretField } from '@/components/secrets/CredentialSecretField'

import {
  machinePoolFormAfterProviderChange,
  type MachinePoolFormMode,
  type MachinePoolFormValues,
  machinePoolProviderLabel,
} from './MachinePoolDialogState'
import { MachinePoolInputField } from './MachinePoolInputField'
import { isMachinePoolProvider, machinePoolProviderDefinitions } from './machinePoolProviders'
import { MachinePoolProviderSelect } from './MachinePoolProviderSelect'
import { MachinePoolResourceFields } from './MachinePoolResourceFields'

export type MachinePoolFormSetValue = <K extends keyof MachinePoolFormValues>(
  key: K,
  value: MachinePoolFormValues[K],
) => void

export function MachinePoolFields({
  orgId,
  enabled,
  mode,
  values,
  setValue,
}: {
  orgId: string
  enabled: boolean
  mode: MachinePoolFormMode
  values: MachinePoolFormValues
  setValue: MachinePoolFormSetValue
}) {
  const definition = machinePoolProviderDefinitions[values.provider]
  const providerEditable = mode === 'create'
  const clusterEdit = mode === 'cluster-edit'

  function changeProvider(providerValue: string) {
    if (!providerEditable || !isMachinePoolProvider(providerValue)) return
    const nextValues = machinePoolFormAfterProviderChange(values, providerValue)
    setValue('provider', nextValues.provider)
    setValue('workspace', nextValues.workspace)
    setValue('image', nextValues.image)
    setValue('location', nextValues.location)
    setValue('cpu', nextValues.cpu)
    setValue('memoryGb', nextValues.memoryGb)
    setValue('maxTotalCpu', nextValues.maxTotalCpu)
    setValue('maxTotalMemoryGb', nextValues.maxTotalMemoryGb)
    setValue('minMachineCpu', nextValues.minMachineCpu)
    setValue('minMachineMemoryGb', nextValues.minMachineMemoryGb)
    setValue('maxMachineCpu', nextValues.maxMachineCpu)
    setValue('maxMachineMemoryGb', nextValues.maxMachineMemoryGb)
    setValue('secretId', nextValues.secretId)
  }

  return (
    <>
      {!clusterEdit && (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <MachinePoolProviderSelect
              value={values.provider}
              disabled={!providerEditable}
              onValueChange={changeProvider}
            />
            <MachinePoolInputField
              id="mpool-name"
              label="Name"
              required
              value={values.name}
              placeholder="default"
              onValueChange={(name) => {
                setValue('name', name)
              }}
            />
          </div>
          <MachinePoolInputField
            id="mpool-description"
            label="Description"
            value={values.description}
            onValueChange={(description) => {
              setValue('description', description)
            }}
          />
          <MachinePoolInputField
            id="mpool-image"
            label={definition.resource.label}
            required
            value={values.image}
            placeholder={definition.resource.placeholder}
            autoComplete="off"
            onValueChange={(image) => {
              setValue('image', image)
            }}
            description={definition.resource.description}
            descriptionHref={definition.resource.descriptionHref}
          />
          {definition.requiresWorkspace && (
            <MachinePoolInputField
              id="mpool-workspace"
              label="Workspace"
              required
              value={values.workspace}
              autoComplete="off"
              onValueChange={(workspace) => {
                setValue('workspace', workspace)
              }}
            />
          )}
        </>
      )}
      <MachinePoolResourceFields
        provider={values.provider}
        clusterManaged={clusterEdit}
        location={values.location}
        cpu={values.cpu}
        memoryGb={values.memoryGb}
        maxMachines={values.maxMachines}
        onLocationChange={(location) => {
          setValue('location', location)
        }}
        onCpuChange={(cpu) => {
          setValue('cpu', cpu)
        }}
        onMemoryGbChange={(memoryGb) => {
          setValue('memoryGb', memoryGb)
        }}
        onMaxMachinesChange={(maxMachines) => {
          setValue('maxMachines', maxMachines)
        }}
      />
      {!clusterEdit && (
        <StartupScriptField
          id="mpool-startup-script"
          label="Startup script (optional)"
          provider={values.provider}
          value={values.startupScript}
          placeholder={'apt-get update\napt-get install -y ripgrep'}
          onChange={(startupScript) => {
            setValue('startupScript', startupScript)
          }}
        />
      )}
      {!clusterEdit && (
        <CredentialSecretField
          key={values.provider}
          orgId={orgId}
          enabled={enabled}
          value={values.secretId}
          onChange={(secretId) => {
            setValue('secretId', secretId)
          }}
          label={`${machinePoolProviderLabel(values.provider)} API token`}
          placeholder={`Search secrets for your ${machinePoolProviderLabel(values.provider)} token…`}
          emptyDescription={`No secrets yet — use New secret to store your ${machinePoolProviderLabel(values.provider)} API token.`}
          defaultSecretName={`${values.provider}-api-token`}
          secretValuePlaceholder="Provider API token"
        />
      )}
    </>
  )
}
