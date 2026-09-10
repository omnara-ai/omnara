import { useUpdateConfiguredModel } from '@omnara/react'
import { type ConfiguredModel } from '@omnara/sdk'
import { useForm } from '@tanstack/react-form'

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
  const mutation = useUpdateConfiguredModel(orgId)
  const defaultValues = {
    name: model.name,
    slug: model.provider_model_slug,
    contextWindowTokens: String(model.context_window_tokens),
    maxOutputTokens: model.max_output_tokens == null ? '' : String(model.max_output_tokens),
    defaultMaxOutputTokens:
      model.default_max_output_tokens == null ? '' : String(model.default_max_output_tokens),
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
      try {
        await mutation.mutateAsync({
          modelProviderConfigID: model.model_provider_config_id,
          configuredModelID: model.id,
          name: value.name === model.name ? undefined : value.name,
          provider_model_slug: value.slug.trim(),
          context_window_tokens: Number(value.contextWindowTokens),
          max_output_tokens: optionalNumber(value.maxOutputTokens),
          default_max_output_tokens: optionalNumber(value.defaultMaxOutputTokens),
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
                  <FieldLabel>Name</FieldLabel>
                  <Input
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
                  <FieldLabel>Provider model slug</FieldLabel>
                  <Input
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
                  <FieldLabel>Context window</FieldLabel>
                  <Input
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
                    <FieldLabel>Maximum output</FieldLabel>
                    <Input
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
                    <FieldLabel>Default output</FieldLabel>
                    <Input
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
            <form.Subscribe selector={(state) => configuredModelTokenLimitsError(state.values)}>
              {(error) => error && <p className="text-destructive text-sm">{error}</p>}
            </form.Subscribe>
            {error && <p className="text-destructive text-sm">{error}</p>}
            <DialogFooter>
              <form.Subscribe selector={(state) => valid(state.values)}>
                {(valid) => (
                  <Button
                    type="submit"
                    disabled={mutation.isPending || !valid}
                    loading={mutation.isPending}
                  >
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
