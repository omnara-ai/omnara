import { useCreateConfiguredModel, useCreateProjectModelGrant } from '@omnara/react'
import {
  type ConfiguredModel,
  type DiscoveredProviderModel,
  type ModelProviderConfig,
} from '@omnara/sdk'
import { useForm } from '@tanstack/react-form'
import { useState } from 'react'

import { ProjectGrantsField } from '@/components/projects/ProjectGrantsField'
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
import { Spinner } from '@/components/ui/spinner'
import { collectGrantFailures, type RetryGrantsPhase } from '@/lib/grant-failures'
import { errorMessage } from '@/lib/submit-status'

import { ConfiguredModelAdvancedFields } from './ConfiguredModelAdvancedFields'
import { ConfiguredModelProviderField } from './ConfiguredModelProviderField'
import {
  configuredModelFormDefaults,
  configuredModelFormValid,
  discoveredModelPrefill,
  providerChangeReset,
} from './CreateConfiguredModelDialogState'
import { ProviderModelSlugField } from './ProviderModelSlugField'

export function CreateConfiguredModelDialog({
  open,
  onOpenChange,
  orgId,
  providers,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  providers: ModelProviderConfig[]
}) {
  const createConfiguredModel = useCreateConfiguredModel(orgId)
  const createProjectModelGrant = useCreateProjectModelGrant(orgId)
  const [advancedOpen, setAdvancedOpen] = useState(false)
  const [phase, setPhase] = useState<RetryGrantsPhase<ConfiguredModel>>({
    kind: 'form',
    error: '',
  })
  const form = useForm({
    defaultValues: configuredModelFormDefaults,
    onSubmit: async ({ value }) => {
      const provider = providers.find((item) => item.id === value.providerId) ?? providers[0]
      if (!provider && phase.kind === 'form') {
        return
      }
      setPhase((prev) => ({ ...prev, error: '' }))
      try {
        let model = phase.kind === 'retry-grants' ? phase.created : null
        if (!model && provider) {
          model = await createConfiguredModel.mutateAsync({
            modelProviderConfigID: provider.id,
            name: value.name.trim(),
            provider_model_slug: value.providerModelSlug.trim(),
            context_window_tokens: Number(value.contextWindowTokens),
            ...(value.maxOutputTokens === ''
              ? {}
              : { max_output_tokens: Number(value.maxOutputTokens) }),
            ...(value.defaultMaxOutputTokens === ''
              ? {}
              : { default_max_output_tokens: Number(value.defaultMaxOutputTokens) }),
            supports_tools: true,
            supports_reasoning: false,
          })
        }
        if (!model) return
        const grantResults = await Promise.allSettled(
          value.projectGrantIds.map((projectID) =>
            createProjectModelGrant.mutateAsync({ projectID, configured_model_id: model.id }),
          ),
        )
        const failures = collectGrantFailures(value.projectGrantIds, grantResults)
        if (failures) {
          form.setFieldValue('projectGrantIds', failures.failedProjectIds)
          setPhase({
            kind: 'retry-grants',
            created: model,
            error: `The model was created, but ${failures.message}`,
          })
          return
        }
        form.reset()
        setAdvancedOpen(false)
        setPhase({ kind: 'form', error: '' })
        onOpenChange(false)
      } catch (err) {
        setPhase((prev) => ({ ...prev, error: errorMessage(err, 'Could not add model') }))
      }
    },
  })

  function providerById(providerId: string) {
    return providers.find((item) => item.id === providerId) ?? providers[0]
  }

  function applyDiscoveredModel(model: DiscoveredProviderModel) {
    for (const [fieldName, fieldValue] of discoveredModelPrefill(
      providerById(form.state.values.providerId)?.name,
      form.state.values,
      model,
    )) {
      form.setFieldValue(fieldName, fieldValue)
    }
  }

  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) {
      setAdvancedOpen(false)
      setPhase({ kind: 'form', error: '' })
    }
    onOpenChange(nextOpen)
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add configured model</DialogTitle>
          <DialogDescription>Create a model for a configured provider.</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void form.handleSubmit()
          }}
        >
          <FieldGroup>
            <form.Field name="providerId">
              {(field) => (
                <ConfiguredModelProviderField
                  providers={providers}
                  value={field.state.value}
                  onChange={(nextValue) => {
                    for (const [fieldName, fieldValue] of providerChangeReset(
                      providerById(field.state.value)?.name,
                      form.state.values,
                    )) {
                      form.setFieldValue(fieldName, fieldValue)
                    }
                    field.handleChange(nextValue)
                  }}
                />
              )}
            </form.Field>
            <form.Field name="name">
              {(field) => (
                <Field>
                  <FieldLabel htmlFor="cm-name">Name</FieldLabel>
                  <Input
                    id="cm-name"
                    required
                    value={field.state.value}
                    placeholder="gpt-5.5"
                    onChange={(event) => {
                      field.handleChange(event.target.value)
                    }}
                  />
                </Field>
              )}
            </form.Field>
            <form.Field name="providerModelSlug">
              {(field) => (
                <form.Subscribe selector={(state) => state.values.providerId}>
                  {(providerId) => (
                    <ProviderModelSlugField
                      id="cm-provider-model-slug"
                      orgId={orgId}
                      provider={providerById(providerId)}
                      enabled={open}
                      value={field.state.value}
                      onChange={field.handleChange}
                      onSelect={applyDiscoveredModel}
                    />
                  )}
                </form.Subscribe>
              )}
            </form.Field>
            <form.Field name="contextWindowTokens">
              {(field) => (
                <Field>
                  <FieldLabel htmlFor="cm-context-window">Context window</FieldLabel>
                  <Input
                    id="cm-context-window"
                    type="number"
                    min="2"
                    step="1"
                    required
                    value={field.state.value}
                    onChange={(event) => {
                      field.handleChange(event.target.value)
                    }}
                  />
                  <FieldDescription>
                    Total context capacity reported by the provider.
                  </FieldDescription>
                </Field>
              )}
            </form.Field>
            <ConfiguredModelAdvancedFields
              open={advancedOpen}
              onToggle={() => {
                setAdvancedOpen((value) => !value)
              }}
            >
              <form.Field name="defaultMaxOutputTokens">
                {(field) => (
                  <Field>
                    <FieldLabel htmlFor="cm-default-output">Default output</FieldLabel>
                    <Input
                      id="cm-default-output"
                      type="number"
                      min="1"
                      step="1"
                      value={field.state.value}
                      onChange={(event) => {
                        field.handleChange(event.target.value)
                      }}
                    />
                    <FieldDescription>
                      Optional; Omnara chooses a conservative default when omitted.
                    </FieldDescription>
                  </Field>
                )}
              </form.Field>
              <form.Field name="maxOutputTokens">
                {(field) => (
                  <Field>
                    <FieldLabel htmlFor="cm-max-output">Max output</FieldLabel>
                    <Input
                      id="cm-max-output"
                      type="number"
                      min="1"
                      step="1"
                      value={field.state.value}
                      onChange={(event) => {
                        field.handleChange(event.target.value)
                      }}
                    />
                    <FieldDescription>
                      Optional hard ceiling, including compaction.
                    </FieldDescription>
                  </Field>
                )}
              </form.Field>
            </ConfiguredModelAdvancedFields>
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
                  [
                    configuredModelFormValid(
                      state.values,
                      providers.find((item) => item.id === state.values.providerId) ?? providers[0],
                    ),
                    state.isSubmitting,
                  ] as const
                }
              >
                {([valid, isSubmitting]) => (
                  <Button
                    type="submit"
                    disabled={isSubmitting || (phase.kind === 'form' && !valid)}
                  >
                    {isSubmitting && <Spinner />}
                    {phase.kind === 'retry-grants' ? 'Retry project grants' : 'Add model'}
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
