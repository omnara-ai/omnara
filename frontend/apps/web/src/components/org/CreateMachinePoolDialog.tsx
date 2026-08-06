import { useCreateMachinePool, useGrantMachinePoolToProject } from '@omnara/react'
import type { MachinePool } from '@omnara/sdk'
import { useForm } from '@tanstack/react-form'
import { useState } from 'react'

import { StartupScriptField } from '@/components/machines/StartupScriptField'
import { ProjectGrantsField } from '@/components/projects/ProjectGrantsField'
import { CredentialSecretField } from '@/components/secrets/CredentialSecretField'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { FieldGroup } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'
import { collectGrantFailures, type RetryGrantsPhase } from '@/lib/grant-failures'
import { errorMessage } from '@/lib/submit-status'

import { CreateMachinePoolAdvancedSection } from './CreateMachinePoolAdvancedSection'
import {
  machinePoolCreateRequest,
  machinePoolFormAfterProviderChange,
  machinePoolFormDefaults,
  machinePoolFormValid,
  machinePoolProviderLabel,
} from './CreateMachinePoolDialogState'
import { MachinePoolInputField } from './MachinePoolInputField'
import { isMachinePoolProvider, machinePoolProviderDefinitions } from './machinePoolProviders'
import { MachinePoolProviderSelect } from './MachinePoolProviderSelect'
import { MachinePoolResourceFields } from './MachinePoolResourceFields'

export function CreateMachinePoolDialog({
  open,
  onOpenChange,
  orgId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
}) {
  const createMachinePool = useCreateMachinePool(orgId)
  const grantMachinePool = useGrantMachinePoolToProject(orgId)
  const [phase, setPhase] = useState<RetryGrantsPhase<MachinePool>>({ kind: 'form', error: '' })
  const form = useForm({
    defaultValues: machinePoolFormDefaults,
    onSubmit: async ({ value }) => {
      setPhase((prev) => ({ ...prev, error: '' }))
      try {
        let pool = phase.kind === 'retry-grants' ? phase.created : null
        pool ??= await createMachinePool.mutateAsync(machinePoolCreateRequest(value))
        const grantResults = await Promise.allSettled(
          value.projectGrantIds.map((projectID) =>
            grantMachinePool.mutateAsync({ projectID, machine_pool_id: pool.id }),
          ),
        )
        const failures = collectGrantFailures(value.projectGrantIds, grantResults)
        if (failures) {
          form.setFieldValue('projectGrantIds', failures.failedProjectIds)
          setPhase({
            kind: 'retry-grants',
            created: pool,
            error: `The pool was created, but ${failures.message}`,
          })
          return
        }
        // Keep the machine sizing so consecutive pools reuse it.
        form.reset({
          ...machinePoolFormDefaults,
          cpu: value.cpu,
          memoryMb: value.memoryMb,
          maxMachines: value.maxMachines,
        })
        setPhase({ kind: 'form', error: '' })
        onOpenChange(false)
      } catch (err) {
        setPhase((prev) => ({ ...prev, error: errorMessage(err, 'Could not create pool') }))
      }
    },
  })
  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) setPhase({ kind: 'form', error: '' })
    onOpenChange(nextOpen)
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-h-[85svh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>New machine pool</DialogTitle>
          <DialogDescription>Pools provision the machines your agents run on.</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void form.handleSubmit()
          }}
        >
          <FieldGroup>
            <div className="grid gap-4 sm:grid-cols-2">
              <form.Field name="provider">
                {(field) => (
                  <MachinePoolProviderSelect
                    value={field.state.value}
                    onValueChange={(provider) => {
                      if (!isMachinePoolProvider(provider)) return
                      const nextValues = machinePoolFormAfterProviderChange(
                        form.state.values,
                        provider,
                      )
                      field.handleChange(nextValues.provider)
                      form.setFieldValue('workspace', nextValues.workspace)
                      form.setFieldValue('image', nextValues.image)
                      form.setFieldValue('location', nextValues.location)
                      form.setFieldValue('cpu', nextValues.cpu)
                      form.setFieldValue('memoryMb', nextValues.memoryMb)
                      form.setFieldValue('maxTotalCpu', nextValues.maxTotalCpu)
                      form.setFieldValue('maxTotalMemoryMb', nextValues.maxTotalMemoryMb)
                      form.setFieldValue('maxMachineCpu', nextValues.maxMachineCpu)
                      form.setFieldValue('maxMachineMemoryMb', nextValues.maxMachineMemoryMb)
                      form.setFieldValue('secretId', nextValues.secretId)
                    }}
                  />
                )}
              </form.Field>
              <form.Field name="name">
                {(field) => (
                  <MachinePoolInputField
                    id="mpool-name"
                    label="Name"
                    required
                    value={field.state.value}
                    placeholder="default"
                    onValueChange={field.handleChange}
                  />
                )}
              </form.Field>
            </div>
            <form.Subscribe selector={(state) => state.values.provider}>
              {(provider) => (
                <form.Field name="image">
                  {(field) => (
                    <MachinePoolInputField
                      id="mpool-image"
                      label={machinePoolProviderDefinitions[provider].resource.label}
                      required
                      value={field.state.value}
                      placeholder={machinePoolProviderDefinitions[provider].resource.placeholder}
                      autoComplete="off"
                      onValueChange={field.handleChange}
                      description={machinePoolProviderDefinitions[provider].resource.description}
                      descriptionHref={
                        machinePoolProviderDefinitions[provider].resource.descriptionHref
                      }
                    />
                  )}
                </form.Field>
              )}
            </form.Subscribe>
            <form.Subscribe selector={(state) => state.values.provider}>
              {(provider) =>
                machinePoolProviderDefinitions[provider].requiresWorkspace ? (
                  <form.Field name="workspace">
                    {(field) => (
                      <MachinePoolInputField
                        id="mpool-workspace"
                        label="Workspace"
                        required
                        value={field.state.value}
                        autoComplete="off"
                        onValueChange={field.handleChange}
                      />
                    )}
                  </form.Field>
                ) : null
              }
            </form.Subscribe>
            <form.Subscribe
              selector={(state) =>
                [
                  state.values.provider,
                  state.values.location,
                  state.values.cpu,
                  state.values.memoryMb,
                  state.values.maxMachines,
                ] as const
              }
            >
              {([provider, location, cpu, memoryMb, maxMachines]) => (
                <MachinePoolResourceFields
                  provider={provider}
                  location={location}
                  cpu={cpu}
                  memoryMb={memoryMb}
                  maxMachines={maxMachines}
                  onLocationChange={(value) => {
                    form.setFieldValue('location', value)
                  }}
                  onCpuChange={(value) => {
                    form.setFieldValue('cpu', value)
                  }}
                  onMemoryMbChange={(value) => {
                    form.setFieldValue('memoryMb', value)
                  }}
                  onMaxMachinesChange={(value) => {
                    form.setFieldValue('maxMachines', value)
                  }}
                />
              )}
            </form.Subscribe>
            <form.Subscribe selector={(state) => state.values.provider}>
              {(provider) => (
                <form.Field name="startupScript">
                  {(field) => (
                    <StartupScriptField
                      id="mpool-startup-script"
                      label="Startup script (optional)"
                      provider={provider}
                      value={field.state.value}
                      placeholder={'apt-get update\napt-get install -y ripgrep'}
                      onChange={field.handleChange}
                    />
                  )}
                </form.Field>
              )}
            </form.Subscribe>
            <form.Subscribe selector={(state) => state.values}>
              {(values) => (
                <CreateMachinePoolAdvancedSection
                  orgId={orgId}
                  enabled={open}
                  values={values}
                  setValue={(key, value) => {
                    form.setFieldValue(key, value as never)
                  }}
                />
              )}
            </form.Subscribe>
            <form.Field name="secretId">
              {(field) => (
                <form.Subscribe selector={(state) => state.values.provider}>
                  {(provider) => (
                    <CredentialSecretField
                      key={provider}
                      orgId={orgId}
                      enabled={open}
                      value={field.state.value}
                      onChange={field.handleChange}
                      label={`${machinePoolProviderLabel(provider)} API token`}
                      placeholder={`Search secrets for your ${machinePoolProviderLabel(provider)} token…`}
                      emptyDescription={`No secrets yet — use New secret to store your ${machinePoolProviderLabel(provider)} API token.`}
                      defaultSecretName={`${provider}-api-token`}
                      secretValuePlaceholder="Provider API token"
                    />
                  )}
                </form.Subscribe>
              )}
            </form.Field>
            <form.Field name="projectGrantIds">
              {(field) => (
                <form.Subscribe selector={(state) => state.isSubmitting}>
                  {(isSubmitting) => (
                    <ProjectGrantsField
                      orgId={orgId}
                      isProjectEligible={(project) => project.access.can_manage_access}
                      value={field.state.value}
                      onChange={field.handleChange}
                      disabled={isSubmitting}
                    />
                  )}
                </form.Subscribe>
              )}
            </form.Field>
            {phase.error && <p className="text-destructive text-sm">{phase.error}</p>}
            <DialogFooter>
              <form.Subscribe
                selector={(state) =>
                  [machinePoolFormValid(state.values), state.isSubmitting] as const
                }
              >
                {([valid, isSubmitting]) => (
                  <Button
                    type="submit"
                    disabled={isSubmitting || (phase.kind === 'form' && !valid)}
                  >
                    {isSubmitting && <Spinner />}
                    {phase.kind === 'retry-grants' ? 'Retry project grants' : 'Create pool'}
                  </Button>
                )}
              </form.Subscribe>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
