import { StartupScriptField } from '@/components/machines/StartupScriptField'
import { CredentialSecretField } from '@/components/secrets/CredentialSecretField'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'

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

interface MachinePoolFieldProps {
  values: MachinePoolFormValues
  setValue: MachinePoolFormSetValue
}

/** Switches provider and resets every field whose meaning depends on it. */
function changeMachinePoolProvider(
  values: MachinePoolFormValues,
  setValue: MachinePoolFormSetValue,
  providerValue: string,
) {
  if (!isMachinePoolProvider(providerValue)) return
  const nextValues = machinePoolFormAfterProviderChange(values, providerValue)
  setValue('provider', nextValues.provider)
  setValue('providerScope', nextValues.providerScope)
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

export function MachinePoolProviderField({
  values,
  setValue,
  disabled = false,
}: MachinePoolFieldProps & { disabled?: boolean }) {
  return (
    <MachinePoolProviderSelect
      value={values.provider}
      disabled={disabled}
      onValueChange={(provider) => {
        if (!disabled) changeMachinePoolProvider(values, setValue, provider)
      }}
    />
  )
}

export function MachinePoolNameField({ values, setValue }: MachinePoolFieldProps) {
  return (
    <MachinePoolInputField
      id="mpool-name"
      label="Name"
      required
      value={values.name}
      placeholder="default"
      onValueChange={(name) => {
        setValue('name', name)
      }}
      error={<ResourceNameFieldError value={values.name} />}
    />
  )
}

export function MachinePoolDescriptionField({ values, setValue }: MachinePoolFieldProps) {
  return (
    <MachinePoolInputField
      id="mpool-description"
      label="Description"
      value={values.description}
      onValueChange={(description) => {
        setValue('description', description)
      }}
    />
  )
}

export function MachinePoolImageField({ values, setValue }: MachinePoolFieldProps) {
  const resource = machinePoolProviderDefinitions[values.provider].resource
  return (
    <MachinePoolInputField
      id="mpool-image"
      label={resource.label}
      required={!resource.optional}
      value={values.image}
      placeholder={resource.placeholder}
      autoComplete="off"
      onValueChange={(image) => {
        setValue('image', image)
      }}
      description={resource.description}
      descriptionHref={resource.descriptionHref}
    />
  )
}

export function MachinePoolScopeField({ values, setValue }: MachinePoolFieldProps) {
  const scope = machinePoolProviderDefinitions[values.provider].scope
  if (!scope) return null
  return (
    <MachinePoolInputField
      id="mpool-provider-scope"
      label={scope.label}
      required={scope.required}
      description={scope.description}
      value={values.providerScope}
      placeholder={scope.placeholder}
      autoComplete="off"
      onValueChange={(providerScope) => {
        setValue('providerScope', providerScope)
      }}
    />
  )
}

export function MachinePoolStartupScriptField({
  values,
  setValue,
  label = 'Startup script (optional)',
}: MachinePoolFieldProps & { label?: string }) {
  return (
    <StartupScriptField
      id="mpool-startup-script"
      label={label}
      provider={values.provider}
      value={values.startupScript}
      placeholder={'apt-get update\napt-get install -y ripgrep'}
      onChange={(startupScript) => {
        setValue('startupScript', startupScript)
      }}
    />
  )
}

export function MachinePoolCredentialField({
  orgId,
  enabled,
  values,
  setValue,
}: MachinePoolFieldProps & { orgId: string; enabled: boolean }) {
  const credential = machinePoolProviderDefinitions[values.provider].credential
  const providerLabel = machinePoolProviderLabel(values.provider)
  return (
    <CredentialSecretField
      key={values.provider}
      orgId={orgId}
      enabled={enabled}
      value={values.secretId}
      onChange={(secretId) => {
        setValue('secretId', secretId)
      }}
      label={credential?.label ?? `${providerLabel} API token`}
      placeholder={credential?.placeholder ?? `Search secrets for your ${providerLabel} token…`}
      emptyDescription={
        credential?.emptyDescription ??
        `No secrets yet — use New secret to store your ${providerLabel} API token.`
      }
      defaultSecretName={credential?.defaultSecretName ?? `${values.provider}-api-token`}
      secretValuePlaceholder={credential?.secretValuePlaceholder ?? 'Provider API token'}
    />
  )
}

export function MachinePoolFields({
  orgId,
  enabled,
  mode,
  values,
  setValue,
}: MachinePoolFieldProps & {
  orgId: string
  enabled: boolean
  mode: MachinePoolFormMode
}) {
  const providerEditable = mode === 'create'
  const clusterEdit = mode === 'cluster-edit'

  return (
    <>
      {!clusterEdit && (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <MachinePoolProviderField
              values={values}
              setValue={setValue}
              disabled={!providerEditable}
            />
            <MachinePoolNameField values={values} setValue={setValue} />
          </div>
          <MachinePoolDescriptionField values={values} setValue={setValue} />
          <MachinePoolImageField values={values} setValue={setValue} />
          <MachinePoolScopeField values={values} setValue={setValue} />
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
      {!clusterEdit && <MachinePoolStartupScriptField values={values} setValue={setValue} />}
      {!clusterEdit && (
        <MachinePoolCredentialField
          orgId={orgId}
          enabled={enabled}
          values={values}
          setValue={setValue}
        />
      )}
    </>
  )
}
