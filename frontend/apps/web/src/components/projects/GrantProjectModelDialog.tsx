import {
  useConfiguredModels,
  useCreateProjectModelGrant,
  useModelProviders,
  useProjectModelGrants,
} from '@omnara/react'
import { type ConfiguredModel, type ModelProviderConfig } from '@omnara/sdk'
import { useState } from 'react'

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
import {
  createResourceCombobox,
  createResourceMultiCombobox,
} from '@/components/ui/resource-combobox'
import { Spinner } from '@/components/ui/spinner'
import { useBatchGrantSubmit } from '@/hooks/use-batch-grant-submit'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'

const ModelProviderCombobox = createResourceCombobox<ModelProviderConfig>({
  itemKey: (provider) => provider.id,
  itemLabel: (provider) => provider.name,
  placeholder: 'Search model providers…',
  emptyMessage: 'No model providers found.',
})

const ConfiguredModelMultiCombobox = createResourceMultiCombobox<ConfiguredModel>({
  itemKey: (model) => model.id,
  itemLabel: (model) => model.name,
  placeholder: 'Search configured models…',
  emptyMessage: 'No ungranted models found.',
})

export function GrantProjectModelDialog({
  open,
  onOpenChange,
  orgId,
  projectId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  projectId: string
}) {
  const [provider, setProvider] = useState<ModelProviderConfig | null>(null)
  const providerSearch = useTypeaheadSearch()
  const createGrant = useCreateProjectModelGrant(orgId)
  const batch = useBatchGrantSubmit<ConfiguredModel>({
    label: 'model grant',
    fallbackError: 'Could not grant models',
    itemKey: (model) => model.id,
    grant: (model) =>
      createGrant.mutateAsync({ projectID: projectId, configured_model_id: model.id }),
    onSuccess: () => {
      onOpenChange(false)
    },
  })
  const providersQuery = useModelProviders(orgId, {
    filters: providerSearch.filters,
    sort: 'name',
    pageSize: 25,
    enabled: open,
  })
  const providers = useInfiniteQueryItems(providersQuery)
  const modelsQuery = useConfiguredModels(orgId, provider?.id ?? '', {
    enabled: open && provider !== null,
  })
  const grantsQuery = useProjectModelGrants(orgId, projectId, { enabled: open })
  const completeGrants = useCompleteInfiniteQueryItems(grantsQuery, open)
  const grantedModelIds = new Set(
    completeGrants.items.map((item) => item.grant.configured_model_id),
  )
  const models = useInfiniteQueryItems(modelsQuery).filter(
    (model) => !grantedModelIds.has(model.id),
  )
  const queryError = providersQuery.isError || modelsQuery.isError || grantsQuery.isError

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Grant models</DialogTitle>
          <DialogDescription>
            Let agents in this project use an organization model.
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            void batch.submit(event)
          }}
        >
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="grant-project-model-provider">Provider</FieldLabel>
              <ModelProviderCombobox
                items={providers}
                value={provider?.id ?? null}
                onValueChange={setProvider}
                search={providerSearch}
                query={providersQuery}
                disabled={batch.isSubmitting || providersQuery.isError}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="grant-project-model">Models</FieldLabel>
              <ConfiguredModelMultiCombobox
                items={models}
                value={batch.items.map((model) => model.id)}
                onValueChange={batch.setItems}
                query={modelsQuery}
                pending={(provider !== null && modelsQuery.isPending) || completeGrants.isPending}
                disabled={
                  provider === null ||
                  batch.isSubmitting ||
                  queryError ||
                  !completeGrants.isComplete
                }
              />
              {!queryError && provider !== null && models.length === 0 && (
                <FieldDescription>
                  This provider has no ungranted configured models.
                </FieldDescription>
              )}
            </Field>
            {queryError && (
              <p className="text-destructive text-sm">
                Could not load grantable models.{' '}
                <button
                  type="button"
                  className="underline"
                  onClick={() => {
                    void Promise.all([
                      providersQuery.refetch(),
                      modelsQuery.refetch(),
                      grantsQuery.refetch(),
                    ])
                  }}
                >
                  Retry
                </button>
              </p>
            )}
            {batch.errorMessage && <p className="text-destructive text-sm">{batch.errorMessage}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={
                  batch.isSubmitting ||
                  batch.items.length === 0 ||
                  queryError ||
                  !completeGrants.isComplete
                }
              >
                {batch.isSubmitting && <Spinner />}
                Grant models
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
