import { KeyValueEditor } from '@/components/key-value/KeyValueEditor'
import { StartupScriptField } from '@/components/machines/StartupScriptField'
import { CredentialSecretField } from '@/components/secrets/CredentialSecretField'

import { MachinePoolAdvancedSection } from './MachinePoolAdvancedSection'
import { MachinePoolCapacityFields } from './MachinePoolCapacityFields'
import {
  machinePoolFormAfterProviderChange,
  type MachinePoolFormMode,
  type MachinePoolFormSetValue,
  type MachinePoolFormValues,
  machinePoolProviderLabel,
} from './MachinePoolDialogState'
import { MachinePoolInputField } from './MachinePoolInputField'
import { isMachinePoolProvider, machinePoolProviderDefinitions } from './machinePoolProviders'
import { MachinePoolProviderSelect } from './MachinePoolProviderSelect'
import {
  type MachinePoolFieldErrors,
  machinePoolFieldErrors,
  type MachinePoolFieldGroup,
  machinePoolFieldGroups,
} from './machinePoolValidation'

interface MachinePoolFieldProps {
  values: MachinePoolFormValues
  setValue: MachinePoolFormSetValue
  errors: MachinePoolFieldErrors
}

interface MachinePoolFieldsProps {
  orgId: string
  enabled: boolean
  mode: MachinePoolFormMode
  values: MachinePoolFormValues
  setValue: MachinePoolFormSetValue
}

/**
 * One section of the pool form. Create shows one per step and edit shows them all, so both
 * render the same fields; `machinePoolFieldGroup` assigns each field to its section.
 */
export function MachinePoolFieldGroupFields({
  group,
  orgId,
  enabled,
  mode,
  values,
  setValue,
}: MachinePoolFieldsProps & { group: MachinePoolFieldGroup }) {
  const errors = machinePoolFieldErrors(values, mode)
  const fieldProps = { values, setValue, errors }
  const clusterManaged = mode === 'cluster-edit'
  switch (group) {
    case 'provider':
      if (clusterManaged) return null
      return (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <MachinePoolNameField {...fieldProps} />
            <MachinePoolProviderField {...fieldProps} disabled={mode !== 'create'} />
          </div>
          <MachinePoolDescriptionField {...fieldProps} />
          <MachinePoolCredentialField orgId={orgId} enabled={enabled} {...fieldProps} />
        </>
      )
    case 'image':
      if (clusterManaged) return null
      return (
        <>
          <MachinePoolImageField {...fieldProps} />
          <MachinePoolScopeField {...fieldProps} />
        </>
      )
    case 'capacity':
      return (
        <>
          <MachinePoolCapacityFields clusterManaged={clusterManaged} {...fieldProps} />
          <MachinePoolAdvancedSection clusterManaged={clusterManaged} {...fieldProps} />
        </>
      )
    case 'environment':
      return (
        <>
          {!clusterManaged && <MachinePoolStartupScriptField {...fieldProps} />}
          <MachinePoolEnvironmentFields
            orgId={orgId}
            enabled={enabled}
            clusterManaged={clusterManaged}
            {...fieldProps}
          />
        </>
      )
  }
}

/** Every section of the pool form, in step order. */
export function MachinePoolFields(props: MachinePoolFieldsProps) {
  return machinePoolFieldGroups.map((group) => (
    <MachinePoolFieldGroupFields key={group} group={group} {...props} />
  ))
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

function MachinePoolProviderField({
  values,
  setValue,
  disabled,
}: MachinePoolFieldProps & { disabled: boolean }) {
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

function MachinePoolNameField({ values, setValue, errors }: MachinePoolFieldProps) {
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
      error={errors.name}
    />
  )
}

function MachinePoolDescriptionField({ values, setValue }: MachinePoolFieldProps) {
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

function MachinePoolImageField({ values, setValue, errors }: MachinePoolFieldProps) {
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
      error={errors.image}
      description={resource.description}
      descriptionHref={resource.descriptionHref}
    />
  )
}

function MachinePoolScopeField({ values, setValue, errors }: MachinePoolFieldProps) {
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
      error={errors.providerScope}
    />
  )
}

function MachinePoolStartupScriptField({ values, setValue }: MachinePoolFieldProps) {
  return (
    <StartupScriptField
      id="mpool-startup-script"
      label="Startup script"
      provider={values.provider}
      value={values.startupScript}
      placeholder={'apt-get update\napt-get install -y ripgrep'}
      onChange={(startupScript) => {
        setValue('startupScript', startupScript)
      }}
    />
  )
}

function MachinePoolCredentialField({
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

/** What every machine starts with: working directory and environment variables. */
function MachinePoolEnvironmentFields({
  orgId,
  enabled,
  clusterManaged,
  values,
  setValue,
}: MachinePoolFieldProps & { orgId: string; enabled: boolean; clusterManaged: boolean }) {
  return (
    <>
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
      <KeyValueEditor
        orgId={orgId}
        enabled={enabled}
        label="Environment variables"
        itemLabel="Variable"
        keyPlaceholder="NAME"
        textRows={values.envRows}
        secretRows={values.secretEnvRows}
        onChange={({ textRows, secretRows }) => {
          setValue('envRows', textRows)
          setValue('secretEnvRows', secretRows)
        }}
      />
    </>
  )
}
