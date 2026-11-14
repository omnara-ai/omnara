import { useUpdateConfiguredModel } from '@omnara/react'
import { type ConfiguredModel, type DiscoveredModelPricing } from '@omnara/sdk'
import { zDiscoveredModelPricing } from '@omnara/sdk/zod'
import { useForm } from '@tanstack/react-form'
import { useId } from 'react'

import { OverridesCollapsible } from '@/components/machines/MachineOverrideFields'
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

interface PricingValues {
  inputPrice: string
  outputPrice: string
  cacheReadPrice: string
  cacheWritePrice: string
}

function pricingFromValues(values: PricingValues): DiscoveredModelPricing | null {
  const input = values.inputPrice.trim()
  const output = values.outputPrice.trim()
  const cacheRead = values.cacheReadPrice.trim()
  const cacheWrite = values.cacheWritePrice.trim()
  if (!input && !output && !cacheRead && !cacheWrite) {
    return null
  }
  return {
    input_usd_per_million: input,
    output_usd_per_million: output,
    ...(cacheRead && { cache_read_input_usd_per_million: cacheRead }),
    ...(cacheWrite && { cache_write_input_usd_per_million: cacheWrite }),
  }
}

function pricingError(values: PricingValues) {
  const pricing = pricingFromValues(values)
  return pricing && !zDiscoveredModelPricing.safeParse(pricing).success
    ? 'Enter input and output prices, and any cache prices, as USD amounts like 0.15.'
    : ''
}

const priceFields = [
  { name: 'inputPrice', label: 'Input price', placeholder: '' },
  { name: 'outputPrice', label: 'Output price', placeholder: '' },
  { name: 'cacheReadPrice', label: 'Cache read price', placeholder: 'Input price' },
  { name: 'cacheWritePrice', label: 'Cache write price', placeholder: 'Input price' },
] as const

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
  const defaultValues = {
    name: model.name,
    slug: model.provider_model_slug,
    contextWindowTokens: String(model.context_window_tokens),
    maxOutputTokens: model.max_output_tokens == null ? '' : String(model.max_output_tokens),
    defaultMaxOutputTokens:
      model.default_max_output_tokens == null ? '' : String(model.default_max_output_tokens),
    inputPrice: model.pricing?.input_usd_per_million ?? '',
    outputPrice: model.pricing?.output_usd_per_million ?? '',
    cacheReadPrice: model.pricing?.cache_read_input_usd_per_million ?? '',
    cacheWritePrice: model.pricing?.cache_write_input_usd_per_million ?? '',
  }
  const valid = (value: typeof defaultValues) =>
    resourceNameValid(value.name) &&
    value.slug.trim() !== '' &&
    !configuredModelTokenLimitsError(value) &&
    !pricingError(value)
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
          pricing: pricingFromValues(value),
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
            <form.Subscribe selector={(state) => configuredModelTokenLimitsError(state.values)}>
              {(error) => error && <p className="text-destructive text-sm">{error}</p>}
            </form.Subscribe>
            <OverridesCollapsible title="Advanced">
              <FieldGroup>
                <div className="grid gap-4 sm:grid-cols-2">
                  {priceFields.map(({ name, label, placeholder }) => (
                    <form.Field key={name} name={name}>
                      {(field) => (
                        <Field>
                          <FieldLabel htmlFor={`${idPrefix}-${name}`}>{label}</FieldLabel>
                          <Input
                            id={`${idPrefix}-${name}`}
                            inputMode="decimal"
                            placeholder={placeholder}
                            value={field.state.value}
                            onChange={(event) => {
                              field.handleChange(event.target.value)
                            }}
                          />
                        </Field>
                      )}
                    </form.Field>
                  ))}
                </div>
                <FieldDescription>
                  Your own prices in USD per million tokens. When set, usage and budgets price this
                  model's calls from them instead of the provider-reported cost.
                </FieldDescription>
              </FieldGroup>
            </OverridesCollapsible>
            <form.Subscribe selector={(state) => pricingError(state.values)}>
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
