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
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import { Spinner } from '@/components/ui/spinner'
import { resourceNameInputMaxLength, resourceNameValid } from '@/lib/resource-name'
import { errorMessage } from '@/lib/submit-status'

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
  const form = useForm({
    defaultValues: {
      name: model.name,
      slug: model.provider_model_slug,
      contextWindow: String(model.context_window_tokens),
      maxOutput: String(model.max_output_tokens),
      defaultOutput:
        model.default_max_output_tokens == null ? '' : String(model.default_max_output_tokens),
    },
    onSubmit: async ({ value }) => {
      try {
        await mutation.mutateAsync({
          modelProviderConfigID: model.model_provider_config_id,
          configuredModelID: model.id,
          name: value.name === model.name ? undefined : value.name,
          provider_model_slug: value.slug.trim(),
          context_window_tokens: Number(value.contextWindow),
          max_output_tokens: Number(value.maxOutput),
          default_max_output_tokens: optionalNumber(value.defaultOutput),
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
                    maxLength={resourceNameInputMaxLength}
                    value={field.state.value}
                    onChange={(event) => {
                      field.handleChange(event.target.value)
                    }}
                  />
                  <ResourceNameFieldError
                    value={field.state.value}
                    validate={field.state.value !== model.name}
                  />
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
            <form.Field name="contextWindow">
              {(field) => (
                <Field>
                  <FieldLabel>Context window</FieldLabel>
                  <Input
                    type="number"
                    min="1"
                    value={field.state.value}
                    onChange={(event) => {
                      field.handleChange(event.target.value)
                    }}
                  />
                </Field>
              )}
            </form.Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <form.Field name="maxOutput">
                {(field) => (
                  <Field>
                    <FieldLabel>Maximum output</FieldLabel>
                    <Input
                      type="number"
                      min="1"
                      required
                      value={field.state.value}
                      onChange={(event) => {
                        field.handleChange(event.target.value)
                      }}
                    />
                  </Field>
                )}
              </form.Field>
              <form.Field name="defaultOutput">
                {(field) => (
                  <Field>
                    <FieldLabel>Default output</FieldLabel>
                    <Input
                      type="number"
                      min="1"
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
            {error && <p className="text-destructive text-sm">{error}</p>}
            <DialogFooter>
              <form.Subscribe selector={(state) => [state.values.name, state.values.slug] as const}>
                {([name, slug]) => (
                  <Button
                    type="submit"
                    disabled={
                      mutation.isPending ||
                      (name !== model.name && !resourceNameValid(name)) ||
                      slug.trim() === ''
                    }
                  >
                    {mutation.isPending && <Spinner />}Save changes
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
