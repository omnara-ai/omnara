import {
  useConfiguredModels,
  useCreateProjectModelGrant,
  useModelProviders,
  useProjectModelGrants,
} from '@omnara/react'
import { type ConfiguredModel, type ModelProviderConfig } from '@omnara/sdk'
import { type ReactNode, useState } from 'react'

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
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { createResourceMultiCombobox } from '@/components/ui/resource-multi-combobox'
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

function useGrantableModels(
  orgId: string,
  projectId: string,
  provider: ModelProviderConfig | null,
  open: boolean,
) {
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
  return { modelsQuery, grantsQuery, completeGrants, models }
}

function QueryErrorNotice({
  visible,
  onRetry,
  children,
}: {
  visible: boolean
  onRetry: () => void
  children: ReactNode
}) {
  if (!visible) return null
  return (
    <p className="text-destructive text-sm">
      {children}{' '}
      <button type="button" className="underline" onClick={onRetry}>
        Retry
      </button>
    </p>
  )
}

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
    grant: async (model) => {
      await createGrant.mutateAsync({ projectID: projectId, configured_model_id: model.id })
    },
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
  const { modelsQuery, grantsQuery, completeGrants, models } = useGrantableModels(
    orgId,
    projectId,
    provider,
    open,
  )
  const queryError = providersQuery.isError || modelsQuery.isError || grantsQuery.isError
  const modelsLocked =
    provider === null || batch.isSubmitting || queryError || !completeGrants.isComplete
  const showEmptyModels = !queryError && provider !== null && models.length === 0

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
                value={provider}
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
                value={batch.items}
                onValueChange={batch.setItems}
                query={modelsQuery}
                pending={(provider !== null && modelsQuery.isPending) || completeGrants.isPending}
                disabled={modelsLocked}
              />
              {showEmptyModels && (
                <FieldDescription>
                  This provider has no ungranted configured models.
                </FieldDescription>
              )}
            </Field>
            <QueryErrorNotice
              visible={queryError}
              onRetry={() => {
                void Promise.all([
                  providersQuery.refetch(),
                  modelsQuery.refetch(),
                  grantsQuery.refetch(),
                ])
              }}
            >
              Could not load grantable models.
            </QueryErrorNotice>
            {batch.errorMessage && <p className="text-destructive text-sm">{batch.errorMessage}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={modelsLocked || batch.items.length === 0}
                loading={batch.isSubmitting}
              >
                Grant models
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
