import { useModelCatalog, useUpdateConfiguredModel } from '@omnara/react'
import { type ConfiguredModel } from '@omnara/sdk'
import { useForm } from '@tanstack/react-form'
import { useId } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
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
import { resourceNameValid } from '@/lib/resource-name'
import { errorMessage } from '@/lib/submit-status'

import { configuredModelTokenLimitsError } from './CreateConfiguredModelDialogState'

function optionalNumber(value: string) {
  return value.trim() === '' ? null : Number(value)
}

export function EditConfiguredModelDialog({
  open,
  onOpenChange,
  orgId,
  model,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  model: ConfiguredModel
}) {
  const idPrefix = useId()
  const mutation = useUpdateConfiguredModel(orgId)
  const catalogQuery = useModelCatalog(orgId, model.model_provider_config_id, { enabled: false })
  const defaultValues = {
    name: model.name,
    slug: model.provider_model_slug,
    contextWindowTokens: String(model.context_window_tokens),
    maxOutputTokens: model.max_output_tokens == null ? '' : String(model.max_output_tokens),
    defaultMaxOutputTokens:
      model.default_max_output_tokens == null ? '' : String(model.default_max_output_tokens),
    supportsReasoning: model.supports_reasoning,
  }
  const valid = (value: typeof defaultValues) =>
    resourceNameValid(value.name) &&
    value.slug.trim() !== '' &&
    !configuredModelTokenLimitsError(value)
  const form = useForm({
    defaultValues,
    onSubmit: async ({ value }) => {
      if (!valid(value)) {
        return
      }
      const slug = value.slug.trim()
      const reasoningUnchanged =
        model.supports_reasoning && value.supportsReasoning && slug === model.provider_model_slug
      const catalog =
        value.supportsReasoning && !reasoningUnchanged
          ? (await catalogQuery.refetch()).data
          : undefined
      const efforts = catalog?.models?.find(
        (entry) => entry.slug === slug,
      )?.supported_reasoning_efforts
      try {
        await mutation.mutateAsync({
          modelProviderConfigID: model.model_provider_config_id,
          configuredModelID: model.id,
          name: value.name === model.name ? undefined : value.name,
          provider_model_slug: slug,
          context_window_tokens: Number(value.contextWindowTokens),
          max_output_tokens: optionalNumber(value.maxOutputTokens),
          default_max_output_tokens: optionalNumber(value.defaultMaxOutputTokens),
          supports_reasoning: value.supportsReasoning,
          supported_reasoning_efforts: reasoningUnchanged ? undefined : (efforts ?? []),
          default_reasoning_effort: reasoningUnchanged ? undefined : '',
        })
        onOpenChange(false)
      } catch {
        // Shown via mutation.error below.
      }
    },
  })
  const error = mutation.isError
    ? errorMessage(mutation.error, 'Could not update configured model')
    : ''

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Edit configured model</DialogTitle>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void form.handleSubmit()
          }}
        >
          <FieldGroup>
            <form.Field name="name">
              {(field) => (
                <Field>
                  <FieldLabel htmlFor={`${idPrefix}-name`}>Name</FieldLabel>
                  <Input
                    id={`${idPrefix}-name`}
                    value={field.state.value}
                    onChange={(event) => {
                      field.handleChange(event.target.value)
                    }}
                  />
                  <ResourceNameFieldError value={field.state.value} />
                </Field>
              )}
            </form.Field>
            <form.Field name="slug">
              {(field) => (
                <Field>
                  <FieldLabel htmlFor={`${idPrefix}-slug`}>Provider model slug</FieldLabel>
                  <Input
                    id={`${idPrefix}-slug`}
                    value={field.state.value}
                    onChange={(event) => {
                      field.handleChange(event.target.value)
                    }}
                  />
                </Field>
              )}
            </form.Field>
            <form.Field name="contextWindowTokens">
              {(field) => (
                <Field>
                  <FieldLabel htmlFor={`${idPrefix}-context-window`}>Context window</FieldLabel>
                  <Input
                    id={`${idPrefix}-context-window`}
                    type="number"
                    min="2"
                    value={field.state.value}
                    onChange={(event) => {
                      field.handleChange(event.target.value)
                    }}
                  />
                </Field>
              )}
            </form.Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <form.Field name="maxOutputTokens">
                {(field) => (
                  <Field>
                    <FieldLabel htmlFor={`${idPrefix}-max-output`}>Maximum output</FieldLabel>
                    <Input
                      id={`${idPrefix}-max-output`}
                      type="number"
                      min="1"
                      step="1"
                      placeholder="Unknown"
                      value={field.state.value}
                      onChange={(event) => {
                        field.handleChange(event.target.value)
                      }}
                    />
                    <FieldDescription>
                      Optional output capacity. Leave blank if unknown.
                    </FieldDescription>
                  </Field>
                )}
              </form.Field>
              <form.Field name="defaultMaxOutputTokens">
                {(field) => (
                  <Field>
                    <FieldLabel htmlFor={`${idPrefix}-default-output`}>Default output</FieldLabel>
                    <Input
                      id={`${idPrefix}-default-output`}
                      type="number"
                      min="1"
                      step="1"
                      placeholder="Optional"
                      value={field.state.value}
                      onChange={(event) => {
                        field.handleChange(event.target.value)
                      }}
                    />
                  </Field>
                )}
              </form.Field>
            </div>
            <form.Field name="supportsReasoning">
              {(field) => (
                <Field>
                  <FieldLabel htmlFor={`${idPrefix}-reasoning`}>Reasoning</FieldLabel>
                  <Select
                    value={field.state.value ? 'enabled' : 'disabled'}
                    onValueChange={(next) => {
                      field.handleChange(next === 'enabled')
                    }}
                  >
                    <SelectTrigger id={`${idPrefix}-reasoning`} className="w-full">
                      <SelectValue>{field.state.value ? 'Enabled' : 'Disabled'}</SelectValue>
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="enabled">Enabled</SelectItem>
                      <SelectItem value="disabled">Disabled</SelectItem>
                    </SelectContent>
                  </Select>
                  <FieldDescription>
                    Lets agents choose a reasoning effort for this model.
                  </FieldDescription>
                </Field>
              )}
            </form.Field>
            <form.Subscribe selector={(state) => configuredModelTokenLimitsError(state.values)}>
              {(error) => error && <p className="text-destructive text-sm">{error}</p>}
            </form.Subscribe>
            {error && <p className="text-destructive text-sm">{error}</p>}
            <DialogFooter>
              <form.Subscribe
                selector={(state) => ({
                  valid: valid(state.values),
                  submitting: state.isSubmitting,
                })}
              >
                {({ valid, submitting }) => (
                  <Button type="submit" disabled={submitting || !valid} loading={submitting}>
                    Save changes
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
