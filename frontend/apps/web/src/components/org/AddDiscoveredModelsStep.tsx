import { useCreateConfiguredModel } from '@omnara/react'
import type { DiscoveredProviderModel, ModelProviderConfig } from '@omnara/sdk'
import { useEffect, useRef, useState } from 'react'

import { Button } from '@/components/ui/button'
import { DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { createResourceMultiCombobox } from '@/components/ui/resource-multi-combobox'
import { errorMessage, settleSubmission } from '@/lib/submit-status'

import {
  canCreateDiscoveredModel,
  configuredModelRequestForDiscoveredModel,
} from './CreateModelProviderDialogState'

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
  createdSlugs: string[]
  error: string
}

const initialAddModelsState: AddModelsState = {
  selectedSlugs: [],
  createdSlugs: [],
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
  const creatableModels = discoveredModels.filter(canCreateDiscoveredModel)
  const [state, setState] = useState(initialAddModelsState)
  const selectedSlugSet = new Set(state.selectedSlugs)
  const createdSlugSet = new Set(state.createdSlugs)
  const availableModels = creatableModels.filter((model) => !createdSlugSet.has(model.slug))
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
    const result = await settleSubmission(() =>
      Promise.allSettled(
        slugs.map((slug) =>
          createConfiguredModel.mutateAsync({
            modelProviderConfigID: provider.id,
            ...configuredModelRequestForDiscoveredModel(
              creatableModels.find((model) => model.slug === slug) ?? { slug },
            ),
          }),
        ),
      ),
    ).finally(() => {
      if (mounted.current) setSubmitting(false)
    })
    if (!mounted.current) return
    if (!result.ok) {
      setState((prev) => ({
        ...prev,
        error: errorMessage(result.error, 'The models could not be created.'),
      }))
      return
    }

    const failedSlugs = slugs.filter((_, index) => result.value[index]?.status === 'rejected')
    const createdSlugs = slugs.filter((_, index) => result.value[index]?.status === 'fulfilled')
    if (failedSlugs.length > 0) {
      const firstFailure = result.value.find(
        (result): result is PromiseRejectedResult => result.status === 'rejected',
      )
      setState((prev) => ({
        ...prev,
        selectedSlugs: failedSlugs,
        createdSlugs: [...prev.createdSlugs, ...createdSlugs],
        error:
          `Created ${String(createdSlugs.length)} of ${String(slugs.length)} models. ` +
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
          Create model configurations for {provider.name}. Discovered slugs are used as model names
          when valid, and provider-reported token limits are used when available. Both can be edited
          later.
        </DialogDescription>
      </DialogHeader>
      <FieldGroup>
        <Field>
          <FieldLabel>Detected models</FieldLabel>
          <DiscoveredModelMultiCombobox
            items={availableModels}
            value={availableModels.filter((model) => selectedSlugSet.has(model.slug))}
            disabled={submitting}
            onValueChange={(models) => {
              setState((prev) => ({
                ...prev,
                selectedSlugs: models.map((model) => model.slug),
              }))
            }}
            emptyMessage={
              creatableModels.length === 0
                ? 'No detected models have valid token limits.'
                : 'All detected models selected.'
            }
          />
          {creatableModels.length < discoveredModels.length && (
            <FieldDescription>
              Models without valid token limits are omitted. You can add them manually and enter
              their capacity.
            </FieldDescription>
          )}
        </Field>
        {state.error && <p className="text-destructive text-sm">{state.error}</p>}
        <DialogFooter>
          <Button type="button" variant="ghost" disabled={submitting} onClick={onDone}>
            {state.createdSlugs.length > 0 ? 'Done' : 'Skip for now'}
          </Button>
          <Button
            type="button"
            disabled={submitting || state.selectedSlugs.length === 0}
            loading={submitting}
            onClick={() => {
              void createSelected()
            }}
          >
            {state.selectedSlugs.length > 0
              ? `Create ${String(state.selectedSlugs.length)} ${state.selectedSlugs.length === 1 ? 'model' : 'models'}`
              : 'Create models'}
          </Button>
        </DialogFooter>
      </FieldGroup>
    </>
  )
}
