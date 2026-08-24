import { useCreateModelProvider, useDeleteModelProvider } from '@omnara/react'
import type {
  CreateModelProviderConfigRequest,
  ModelCatalog,
  ModelProviderConfig,
} from '@omnara/sdk'
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
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError } from '@/lib/submit-status'

import { AddDiscoveredModelsStep } from './AddDiscoveredModelsStep'
import {
  awsRegionPattern,
  type BedrockAPI,
  bedrockAPIOption,
  bedrockAPIOptions,
  createModelProviderFormDefaults,
  createModelProviderFormValid,
  type CreateModelProviderFormValues,
  type ModelProviderOption,
  modelProviderOption,
  modelProviderOptions,
  providerSecretName,
} from './CreateModelProviderDialogState'
import { ModelDiscoveryFailureStep } from './ModelDiscoveryFailureStep'

type DialogPhase =
  | { step: 'provider' }
  | {
      step: 'models'
      provider: ModelProviderConfig
      discovery: ModelCatalog
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

  async function submitProvider(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    const submissionGeneration = ++providerSubmissionGeneration.current
    setStatus(idle)
    try {
      const common = {
        name: values.name,
        credential_secret_id: values.secretId,
      }
      let request: CreateModelProviderConfigRequest
      if (values.provider === 'bedrock') {
        const api = bedrockAPIOption(values.bedrockAPI)
        request = {
          ...common,
          api_format: api.apiFormat,
          api_variant: 'bedrock',
          base_url: `https://bedrock-mantle.${values.region.trim()}.api.aws${api.basePath}`,
        }
      } else {
        request = { ...common, preset: values.provider }
      }
      const result = await createModelProvider.mutateAsync(request)
      if (submissionGeneration !== providerSubmissionGeneration.current) return
      if (result.config.api_variant === 'bedrock' && result.model_catalog.status === 'ok') {
        close()
        return
      }
      setPhase({
        step: 'models',
        provider: result.config,
        discovery: result.model_catalog,
        providerCreated: result.created,
      })
    } catch (err) {
      if (submissionGeneration !== providerSubmissionGeneration.current) return
      setStatus(submitError(err, 'Could not add provider'))
    }
  }

  async function backFromDiscovery(providerID: string, providerCreated: boolean) {
    if (providerCreated) {
      await deleteModelProvider.mutateAsync(providerID)
    }
    setPhase({ step: 'provider' })
  }

  const provider = modelProviderOption(values.provider)
  const regionValid = awsRegionPattern.test(values.region.trim())
  const providerPending = createModelProvider.isPending || deleteModelProvider.isPending

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="max-h-[calc(100vh-2rem)] overflow-y-auto sm:max-w-xl">
        {phase.step === 'provider' ? (
          <>
            <DialogHeader>
              <DialogTitle>Add model provider</DialogTitle>
              <DialogDescription>
                Connect OpenAI, OpenRouter, Anthropic, or Amazon Bedrock.
              </DialogDescription>
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
                      value={values.provider}
                      onValueChange={(provider) => {
                        setValues((prev) => ({
                          ...prev,
                          provider: provider as ModelProviderOption,
                          secretId: '',
                        }))
                      }}
                    >
                      <SelectTrigger id="mp-provider" className="w-full">
                        <SelectValue>{provider.label}</SelectValue>
                      </SelectTrigger>
                      <SelectContent>
                        {modelProviderOptions.map((option) => (
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
                      placeholder={`Production ${provider.label}`}
                      onChange={(event) => {
                        setValues((prev) => ({ ...prev, name: event.target.value }))
                      }}
                    />
                    <ResourceNameFieldError value={values.name} />
                  </Field>
                </div>
                {values.provider === 'bedrock' && (
                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field>
                      <FieldLabel htmlFor="mp-bedrock-api">API and endpoint</FieldLabel>
                      <Select
                        value={values.bedrockAPI}
                        onValueChange={(bedrockAPI) => {
                          setValues((prev) => ({
                            ...prev,
                            bedrockAPI: bedrockAPI as BedrockAPI,
                          }))
                        }}
                      >
                        <SelectTrigger id="mp-bedrock-api" className="w-full">
                          <SelectValue>{bedrockAPIOption(values.bedrockAPI).label}</SelectValue>
                        </SelectTrigger>
                        <SelectContent>
                          {bedrockAPIOptions.map((option) => (
                            <SelectItem key={option.value} value={option.value}>
                              {option.label}
                            </SelectItem>
                          ))}
                        </SelectContent>
                      </Select>
                      <FieldDescription>
                        Use the endpoint listed for the model by AWS.
                      </FieldDescription>
                    </Field>
                    <Field>
                      <FieldLabel htmlFor="mp-region">AWS region</FieldLabel>
                      <Input
                        id="mp-region"
                        required
                        autoCapitalize="none"
                        autoCorrect="off"
                        spellCheck={false}
                        pattern={awsRegionPattern.source}
                        value={values.region}
                        placeholder="us-west-2"
                        aria-invalid={!regionValid}
                        onChange={(event) => {
                          setValues((prev) => ({ ...prev, region: event.target.value }))
                        }}
                      />
                      <FieldDescription>
                        {regionValid
                          ? 'The region where your Bedrock API key was generated.'
                          : 'Enter an AWS region such as us-west-2.'}
                      </FieldDescription>
                    </Field>
                  </div>
                )}
                <CredentialSecretField
                  key={values.provider}
                  orgId={orgId}
                  enabled={open}
                  value={values.secretId}
                  onChange={(secretId) => {
                    setValues((prev) =>
                      prev.provider === provider.value ? { ...prev, secretId } : prev,
                    )
                  }}
                  label={`${provider.label} API key`}
                  placeholder={`Search secrets for your ${provider.label} API key…`}
                  emptyDescription={`No secrets yet — use New secret to store your ${provider.label} API key.`}
                  defaultSecretName={providerSecretName(values.provider)}
                  secretValuePlaceholder={provider.keyPlaceholder}
                />
                {statusError(status) && (
                  <p className="text-destructive text-sm">{statusError(status)}</p>
                )}
                <DialogFooter>
                  <Button
                    type="submit"
                    disabled={providerPending || !createModelProviderFormValid(values)}
                    loading={providerPending}
                  >
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
