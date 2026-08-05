import { useCreateModelProvider, useDeleteModelProvider } from '@omnara/react'
import type { ModelDiscoveryResult, ModelProviderConfig } from '@omnara/sdk'
import { type SyntheticEvent, useRef, useState } from 'react'

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
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError } from '@/lib/submit-status'

import { AddDiscoveredModelsStep } from './AddDiscoveredModelsStep'
import {
  createModelProviderFormDefaults,
  createModelProviderFormValid,
  type CreateModelProviderFormValues,
  type ModelProviderPreset,
  modelProviderPresetOption,
  modelProviderPresets,
  presetSecretName,
} from './CreateModelProviderDialogState'
import { ModelDiscoveryFailureStep } from './ModelDiscoveryFailureStep'

type DialogPhase =
  | { step: 'provider' }
  | {
      step: 'models'
      provider: ModelProviderConfig
      discovery: ModelDiscoveryResult
      providerCreated: boolean
    }

export function CreateModelProviderDialog({
  open,
  onOpenChange,
  orgId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
}) {
  const createModelProvider = useCreateModelProvider(orgId)
  const deleteModelProvider = useDeleteModelProvider(orgId)
  const [phase, setPhase] = useState<DialogPhase>({ step: 'provider' })
  const [values, setValues] = useState<CreateModelProviderFormValues>(
    createModelProviderFormDefaults,
  )
  const [status, setStatus] = useState<SubmitStatus>(idle)
  const providerSubmissionGeneration = useRef(0)

  async function submitProvider(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    const submissionGeneration = ++providerSubmissionGeneration.current
    setStatus(idle)
    try {
      const result = await createModelProvider.mutateAsync({
        name: values.name.trim(),
        preset: values.preset,
        credential_secret_id: values.secretId,
      })
      if (submissionGeneration !== providerSubmissionGeneration.current) return
      setPhase({
        step: 'models',
        provider: result.config,
        discovery: result.model_discovery,
        providerCreated: result.created,
      })
    } catch (err) {
      if (submissionGeneration !== providerSubmissionGeneration.current) return
      setStatus(submitError(err, 'Could not add provider'))
    }
  }

  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) {
      // Invalidate an in-flight submission so its late result cannot restore a stale phase.
      providerSubmissionGeneration.current += 1
      setPhase({ step: 'provider' })
      setValues(createModelProviderFormDefaults)
      setStatus(idle)
    }
    onOpenChange(nextOpen)
  }

  function close() {
    handleOpenChange(false)
  }

  async function backFromDiscovery(providerID: string, providerCreated: boolean) {
    if (providerCreated) {
      await deleteModelProvider.mutateAsync(providerID)
    }
    setPhase({ step: 'provider' })
  }

  const preset = modelProviderPresetOption(values.preset)

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-xl">
        {phase.step === 'provider' ? (
          <>
            <DialogHeader>
              <DialogTitle>Add model provider</DialogTitle>
              <DialogDescription>Connect OpenAI, OpenRouter, or Anthropic.</DialogDescription>
            </DialogHeader>
            <form
              onSubmit={(event) => {
                void submitProvider(event)
              }}
            >
              <FieldGroup>
                <div className="grid gap-4 sm:grid-cols-2">
                  <Field>
                    <FieldLabel htmlFor="mp-provider">Provider</FieldLabel>
                    <Select
                      value={values.preset}
                      onValueChange={(preset) => {
                        setValues((prev) => ({
                          ...prev,
                          preset: preset as ModelProviderPreset,
                        }))
                      }}
                    >
                      <SelectTrigger id="mp-provider" className="w-full">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {modelProviderPresets.map((option) => (
                          <SelectItem key={option.value} value={option.value}>
                            {option.label}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </Field>
                  <Field>
                    <FieldLabel htmlFor="mp-name">Name</FieldLabel>
                    <Input
                      id="mp-name"
                      required
                      value={values.name}
                      placeholder={`Production ${preset.label}`}
                      onChange={(event) => {
                        setValues((prev) => ({ ...prev, name: event.target.value }))
                      }}
                    />
                  </Field>
                </div>
                <CredentialSecretField
                  orgId={orgId}
                  enabled={open}
                  value={values.secretId}
                  onChange={(secretId) => {
                    setValues((prev) => ({ ...prev, secretId }))
                  }}
                  label={`${preset.label} API key`}
                  placeholder={`Search secrets for your ${preset.label} API key…`}
                  emptyDescription={`No secrets yet — use New secret to store your ${preset.label} API key.`}
                  defaultSecretName={presetSecretName(values.preset)}
                  secretValuePlaceholder={preset.keyPlaceholder}
                />
                {statusError(status) && (
                  <p className="text-destructive text-sm">{statusError(status)}</p>
                )}
                <DialogFooter>
                  <Button
                    type="submit"
                    disabled={
                      createModelProvider.isPending || !createModelProviderFormValid(values)
                    }
                  >
                    {createModelProvider.isPending && <Spinner />}
                    Add provider
                  </Button>
                </DialogFooter>
              </FieldGroup>
            </form>
          </>
        ) : phase.discovery.status === 'ok' && (phase.discovery.models?.length ?? 0) > 0 ? (
          <AddDiscoveredModelsStep
            orgId={orgId}
            provider={phase.provider}
            discoveredModels={phase.discovery.models ?? []}
            onDone={close}
          />
        ) : (
          <ModelDiscoveryFailureStep
            deleting={phase.providerCreated && deleteModelProvider.isPending}
            providerCreated={phase.providerCreated}
            onBack={() => backFromDiscovery(phase.provider.id, phase.providerCreated)}
            onContinue={close}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}
