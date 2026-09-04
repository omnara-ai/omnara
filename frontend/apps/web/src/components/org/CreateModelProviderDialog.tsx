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
  bedrockAPIOption,
  bedrockAPIOptions,
  bedrockAuthOption,
  bedrockAuthOptions,
  createModelProviderFormDefaults,
  createModelProviderFormValid,
  type CreateModelProviderFormValues,
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

function modelProviderRequest(
  values: CreateModelProviderFormValues,
): CreateModelProviderConfigRequest {
  const common = {
    name: values.name,
    credential_secret_id: values.secretId,
  }
  if (values.provider !== 'bedrock') return { ...common, preset: values.provider }

  const api = bedrockAPIOption(values.bedrockAPI)
  const region = values.region.trim()
  const request: CreateModelProviderConfigRequest = {
    ...common,
    api_format: api.apiFormat,
    api_variant: 'bedrock',
    base_url: `https://bedrock-mantle.${region}.api.aws${api.basePath}`,
  }
  if (values.bedrockAuth !== 'sigv4') return request
  return {
    ...request,
    auth_kind: 'sigv4',
    auth_options: { service: 'bedrock-mantle', region },
  }
}

function BedrockProviderFields({
  values,
  onChange,
}: {
  values: CreateModelProviderFormValues
  onChange: (patch: Partial<CreateModelProviderFormValues>) => void
}) {
  const regionValid = awsRegionPattern.test(values.region.trim())
  const sigv4 = values.bedrockAuth === 'sigv4'

  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field>
        <FieldLabel htmlFor="mp-bedrock-api">API and endpoint</FieldLabel>
        <Select
          value={values.bedrockAPI}
          onValueChange={(value) => {
            const option = bedrockAPIOptions.find((candidate) => candidate.value === value)
            if (!option) return
            onChange({ bedrockAPI: option.value })
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
        <FieldDescription>Use the endpoint listed for the model by AWS.</FieldDescription>
      </Field>
      <Field>
        <FieldLabel htmlFor="mp-bedrock-auth">Authentication</FieldLabel>
        <Select
          value={values.bedrockAuth}
          onValueChange={(value) => {
            const option = bedrockAuthOptions.find((candidate) => candidate.value === value)
            if (!option) return
            onChange({ bedrockAuth: option.value, secretId: '' })
          }}
        >
          <SelectTrigger id="mp-bedrock-auth" className="w-full">
            <SelectValue>{bedrockAuthOption(values.bedrockAuth).label}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {bedrockAuthOptions.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
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
            onChange({ region: event.target.value })
          }}
        />
        <FieldDescription>
          {!regionValid
            ? 'Enter an AWS region such as us-west-2.'
            : sigv4
              ? 'The AWS region used to sign model requests.'
              : 'The region where your Bedrock API key was generated.'}
        </FieldDescription>
      </Field>
    </div>
  )
}

function credentialFieldCopy(
  values: CreateModelProviderFormValues,
  provider: ReturnType<typeof modelProviderOption>,
) {
  if (values.provider === 'bedrock' && values.bedrockAuth === 'sigv4') {
    return {
      label: 'AWS credentials',
      placeholder: 'Search AWS credential secrets…',
      emptyDescription: 'No AWS credentials yet — use New secret to create one.',
      defaultSecretName: 'bedrock-aws-credentials',
      kind: 'aws_credentials' as const,
    }
  }
  return {
    label: `${provider.label} API key`,
    placeholder: `Search secrets for your ${provider.label} API key…`,
    emptyDescription: `No secrets yet — use New secret to store your ${provider.label} API key.`,
    defaultSecretName: providerSecretName(values.provider),
    kind: 'generic' as const,
  }
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
      const result = await createModelProvider.mutateAsync(modelProviderRequest(values))
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
  const credential = credentialFieldCopy(values, provider)
  const providerPending = createModelProvider.isPending || deleteModelProvider.isPending

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-xl">
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
                      onValueChange={(value) => {
                        const option = modelProviderOptions.find(
                          (candidate) => candidate.value === value,
                        )
                        if (!option) return
                        setValues((prev) => ({ ...prev, provider: option.value, secretId: '' }))
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
                  <BedrockProviderFields
                    values={values}
                    onChange={(patch) => {
                      setValues((prev) => ({ ...prev, ...patch }))
                    }}
                  />
                )}
                <CredentialSecretField
                  key={`${values.provider}-${values.bedrockAuth}`}
                  orgId={orgId}
                  enabled={open}
                  value={values.secretId}
                  onChange={(secretId) => {
                    setValues((prev) =>
                      prev.provider === provider.value ? { ...prev, secretId } : prev,
                    )
                  }}
                  label={credential.label}
                  placeholder={credential.placeholder}
                  emptyDescription={credential.emptyDescription}
                  defaultSecretName={credential.defaultSecretName}
                  secretValuePlaceholder={provider.keyPlaceholder}
                  kind={credential.kind}
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
