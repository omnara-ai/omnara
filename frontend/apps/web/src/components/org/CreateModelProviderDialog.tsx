import { useCreateModelProvider } from '@omnara/react'
import type {
  CreateModelProviderConfigRequest,
  ModelCatalog,
  ModelProviderConfig,
} from '@omnara/sdk'
import { type SyntheticEvent, useRef, useState } from 'react'

import { KeyValueEditor } from '@/components/key-value/KeyValueEditor'
import { recordFromSecretRows, recordFromTextRows } from '@/components/key-value/keyValueRows'
import { OverridesCollapsible } from '@/components/machines/MachineOverrideFields'
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
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError } from '@/lib/submit-status'

import { AddConfiguredModelsView } from './AddConfiguredModelsView'
import {
  bedrockAPIOption,
  bedrockBaseUrl,
  createModelProviderFormDefaults,
  createModelProviderFormValid,
  type CreateModelProviderFormValues,
  modelProviderOption,
  providerSecretName,
} from './CreateModelProviderDialogState'
import { CustomProviderFields, ModelProviderEndpointSettings } from './ModelProviderEndpoint'
import { ModelProviderTypeSelect } from './ModelProviderTypeSelect'

type DialogPhase =
  | { step: 'provider' }
  | {
      step: 'models'
      provider: ModelProviderConfig
      discovery: ModelCatalog
    }

function modelProviderRequest(
  values: CreateModelProviderFormValues,
): CreateModelProviderConfigRequest {
  const common = {
    name: values.name,
    credential_secret_id: values.secretId,
    headers: recordFromTextRows(values.headerRows),
    secret_headers: recordFromSecretRows(values.secretHeaderRows),
  }
  if (values.provider === 'custom') {
    return { ...common, api_format: values.apiFormat, base_url: values.baseUrl.trim() }
  }
  if (values.provider !== 'bedrock') return { ...common, preset: values.provider }

  const api = bedrockAPIOption(values.bedrockAPI)
  const region = values.region.trim()
  const request: CreateModelProviderConfigRequest = {
    ...common,
    api_format: api.apiFormat,
    api_variant: 'bedrock',
    base_url: bedrockBaseUrl(values.bedrockAPI, region),
  }
  if (values.bedrockAuth !== 'sigv4') return request
  return {
    ...request,
    auth_kind: 'sigv4',
    auth_options: { service: 'bedrock-mantle', region },
  }
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
    label: 'API key',
    placeholder: 'Search secrets…',
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
      setPhase({
        step: 'models',
        provider: result.config,
        discovery: result.model_catalog,
      })
    } catch (err) {
      if (submissionGeneration !== providerSubmissionGeneration.current) return
      setStatus(submitError(err, 'Could not add provider'))
    }
  }

  const provider = modelProviderOption(values.provider)
  const credential = credentialFieldCopy(values, provider)
  const providerPending = createModelProvider.isPending

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        {phase.step === 'provider' ? (
          <>
            <DialogHeader>
              <DialogTitle>Add model provider</DialogTitle>
              <DialogDescription className="sr-only">
                Connect OpenAI, OpenRouter, Anthropic, Amazon Bedrock, or a custom endpoint.
              </DialogDescription>
            </DialogHeader>
            <form
              onSubmit={(event) => {
                void submitProvider(event)
              }}
            >
              <FieldGroup>
                <ModelProviderTypeSelect
                  value={values.provider}
                  onValueChange={(nextProvider) => {
                    setValues((prev) => ({ ...prev, provider: nextProvider, secretId: '' }))
                  }}
                />
                <div className="grid items-start gap-4 sm:grid-cols-2">
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
                </div>
                {values.provider === 'custom' && (
                  <CustomProviderFields
                    values={values}
                    onChange={(patch) => {
                      setValues((prev) => ({ ...prev, ...patch }))
                    }}
                  />
                )}
                <OverridesCollapsible title="Advanced">
                  <FieldGroup>
                    <ModelProviderEndpointSettings
                      values={values}
                      onChange={(patch) => {
                        setValues((prev) => ({ ...prev, ...patch }))
                      }}
                    />
                    <KeyValueEditor
                      orgId={orgId}
                      enabled={open}
                      label="Headers"
                      itemLabel="Header"
                      keyPlaceholder="Header-Name"
                      textRows={values.headerRows}
                      secretRows={values.secretHeaderRows}
                      onChange={({ textRows, secretRows }) => {
                        setValues((prev) => ({
                          ...prev,
                          headerRows: textRows,
                          secretHeaderRows: secretRows,
                        }))
                      }}
                    />
                  </FieldGroup>
                </OverridesCollapsible>
                {statusError(status) && (
                  <p className="text-destructive text-sm">{statusError(status)}</p>
                )}
                <DialogFooter className="-mx-4 border-t px-4 pt-4 sm:-mx-6 sm:px-6">
                  <Button type="button" variant="outline" onClick={close}>
                    Cancel
                  </Button>
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
        ) : (
          <AddConfiguredModelsView
            orgId={orgId}
            providers={[phase.provider]}
            defaultProviderId={phase.provider.id}
            initialCatalog={phase.discovery}
            dismissLabel="Skip for now"
            onDone={close}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}
