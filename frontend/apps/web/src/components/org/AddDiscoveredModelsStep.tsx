import { useCreateConfiguredModel } from '@omnara/react'
import type { DiscoveredProviderModel, ModelProviderConfig } from '@omnara/sdk'
import { useEffect, useRef, useState } from 'react'

import { Button } from '@/components/ui/button'
import { DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { createResourceMultiCombobox } from '@/components/ui/resource-multi-combobox'
import { Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/lib/submit-status'

import { configuredModelRequestForDiscoveredModel } from './CreateModelProviderDialogState'

const DiscoveredModelMultiCombobox = createResourceMultiCombobox<DiscoveredProviderModel>({
  itemKey: (model) => model.slug,
  itemLabel: (model) =>
    model.display_name && model.display_name !== model.slug
      ? `${model.display_name} (${model.slug})`
      : model.slug,
  placeholder: 'Search detected models…',
  emptyMessage: 'No detected models match.',
})

interface AddModelsState {
  selectedSlugs: string[]
  createdCount: number
  error: string
}

const initialAddModelsState: AddModelsState = {
  selectedSlugs: [],
  createdCount: 0,
  error: '',
}

export function AddDiscoveredModelsStep({
  orgId,
  provider,
  discoveredModels,
  onDone,
}: {
  orgId: string
  provider: ModelProviderConfig
  discoveredModels: DiscoveredProviderModel[]
  onDone: () => void
}) {
  const createConfiguredModel = useCreateConfiguredModel(orgId)
  const creatableModels = discoveredModels.filter(
    (model) => model.context_window_tokens !== undefined && model.context_window_tokens > 1,
  )
  const unavailableCount = discoveredModels.length - creatableModels.length
  const [state, setState] = useState(initialAddModelsState)
  const selectedSlugSet = new Set(state.selectedSlugs)
  // Covers the whole batch below; the mutation's isPending only tracks its latest call.
  const [submitting, setSubmitting] = useState(false)
  const mounted = useRef(true)

  useEffect(() => {
    mounted.current = true
    return () => {
      mounted.current = false
    }
  }, [])

  async function createSelected() {
    const slugs = state.selectedSlugs
    setState((prev) => ({ ...prev, error: '' }))
    setSubmitting(true)
    const results = await Promise.allSettled(
      slugs.map((slug) =>
        createConfiguredModel.mutateAsync({
          modelProviderConfigID: provider.id,
          ...configuredModelRequestForDiscoveredModel(
            provider.name,
            creatableModels.find((model) => model.slug === slug) ?? { slug },
          ),
        }),
      ),
    )
    if (!mounted.current) return
    setSubmitting(false)
    const failedSlugs = slugs.filter((_, index) => results[index]?.status === 'rejected')
    const succeeded = slugs.length - failedSlugs.length
    if (failedSlugs.length > 0) {
      const firstFailure = results.find(
        (result): result is PromiseRejectedResult => result.status === 'rejected',
      )
      setState((prev) => ({
        ...prev,
        selectedSlugs: failedSlugs,
        createdCount: prev.createdCount + succeeded,
        error:
          `Created ${String(succeeded)} of ${String(slugs.length)} models. ` +
          errorMessage(firstFailure?.reason, 'The remaining models could not be created.'),
      }))
      return
    }
    onDone()
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>Add models</DialogTitle>
        <DialogDescription>
          Create model configurations for {provider.name}. Provider-reported token limits are used
          when available and can be edited later. Model names are prefixed with {provider.name} to
          keep them distinct.
        </DialogDescription>
      </DialogHeader>
      <FieldGroup>
        <Field>
          <FieldLabel>Detected models</FieldLabel>
          <DiscoveredModelMultiCombobox
            items={creatableModels}
            value={creatableModels.filter((model) => selectedSlugSet.has(model.slug))}
            disabled={submitting}
            onValueChange={(models) => {
              setState((prev) => ({
                ...prev,
                selectedSlugs: models.map((model) => model.slug),
              }))
            }}
            emptyMessage="All detected models selected."
          />
          <FieldDescription>
            {creatableModels.length} models include the context limit needed for automatic setup.
            {unavailableCount > 0 &&
              ` Add ${String(unavailableCount)} without reported limits manually.`}
          </FieldDescription>
        </Field>
        {state.error && <p className="text-destructive text-sm">{state.error}</p>}
        <DialogFooter>
          <Button type="button" variant="ghost" disabled={submitting} onClick={onDone}>
            {state.createdCount > 0 ? 'Done' : 'Skip for now'}
          </Button>
          <Button
            type="button"
            disabled={submitting || state.selectedSlugs.length === 0}
            onClick={() => {
              void createSelected()
            }}
          >
            {submitting && <Spinner />}
            {state.selectedSlugs.length > 0
              ? `Create ${String(state.selectedSlugs.length)} ${state.selectedSlugs.length === 1 ? 'model' : 'models'}`
              : 'Create models'}
          </Button>
        </DialogFooter>
      </FieldGroup>
    </>
  )
}
