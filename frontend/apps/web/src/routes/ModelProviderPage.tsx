import { useClusterModelPricing, useConfiguredModels, useModelProvider } from '@omnara/react'
import type { ModelProviderConfig } from '@omnara/sdk'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'

import { AgentCard, AgentCardStat, AgentCardTime } from '@/components/agents/AgentCardList'
import { OmnaraManagedTag } from '@/components/brand/OmnaraManaged'
import { DataTablePagination } from '@/components/data-table/DataTable'
import { Box } from '@/components/icons'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { SectionTitle } from '@/components/layout/SectionTitle'
import {
  type ModelDialog,
  ModelDialogs,
  ProviderActions,
} from '@/components/overview/ModelManagement'
import { ProviderGlyph, ProviderSubtitle } from '@/components/overview/ModelProvidersSection'
import { AddModelButton, ProviderModelList } from '@/components/overview/ProviderModelList'
import { Button } from '@/components/ui/button'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { useModelActions } from '@/hooks/use-model-actions'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { formatCount } from '@/lib/format'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function ModelProviderPage() {
  const { activeOrg } = useActiveOrg()
  const { providerId = '' } = useParams({ strict: false })
  const providerQuery = useModelProvider(activeOrg.id, providerId)
  const provider = providerQuery.data

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb
        items={[
          { id: 'models', label: 'Models', to: '/models' },
          { id: 'provider', label: provider?.name ?? 'Provider' },
        ]}
      />
      {provider ? (
        <ProviderView provider={provider} />
      ) : providerQuery.isError ? (
        <Empty className="rounded-xl border">
          <EmptyHeader>
            <EmptyDescription>
              Couldn&rsquo;t load this provider. It may have been deleted.
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent className="flex-row justify-center gap-2">
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                void providerQuery.refetch()
              }}
            >
              Retry
            </Button>
            <Button asChild size="sm" variant="ghost">
              <Link to="/models">Back to models</Link>
            </Button>
          </EmptyContent>
        </Empty>
      ) : (
        <Skeleton className="h-[7.25rem] rounded-xl" />
      )}
    </div>
  )
}

function ProviderView({ provider }: { provider: ModelProviderConfig }) {
  const { activeOrg } = useActiveOrg()
  const canManage = canManageOrg(activeOrg.role)
  const navigate = useNavigate()
  const pricing = useClusterModelPricing(activeOrg.id)
  const modelsQuery = useConfiguredModels(activeOrg.id, provider.id)
  const paged = usePagedQuery(modelsQuery)
  // Counts only the pages fetched so far; "+" marks that more exist.
  const loadedCount = paged.loaded.length
  const [dialog, setDialog] = useState<ModelDialog>(null)
  const modelActions = useModelActions(activeOrg.id, canManage, setDialog)

  return (
    <>
      <AgentCard
        icon={<ProviderGlyph provider={provider} />}
        title={
          <>
            <h1 className="truncate font-medium">{provider.name}</h1>
            {provider.management_kind === 'cluster' && <OmnaraManagedTag />}
          </>
        }
        subtitle={<ProviderSubtitle provider={provider} />}
        meta={
          <>
            <AgentCardTime label="Updated" value={provider.updated_at} />
            {canManage && (
              <ProviderActions
                orgId={activeOrg.id}
                provider={provider}
                onEdit={() => {
                  setDialog({ kind: 'edit-provider', provider })
                }}
                onDeleted={() => {
                  void navigate({ to: '/models' })
                }}
              />
            )}
          </>
        }
        stats={
          <AgentCardStat
            icon={Box}
            label={loadedCount === 1 && !modelsQuery.hasNextPage ? 'model' : 'models'}
            value={
              modelsQuery.isPending || modelsQuery.isError
                ? undefined
                : `${formatCount(loadedCount)}${modelsQuery.hasNextPage ? '+' : ''}`
            }
          />
        }
      />
      <section className="flex flex-col gap-3">
        <div className="flex items-center justify-between gap-2">
          <SectionTitle title="Models" />
          {modelActions.onCreate && (
            <AddModelButton providerId={provider.id} onCreate={modelActions.onCreate} />
          )}
        </div>
        <div className="rounded-xl border [&>div]:border-t-0">
          <ProviderModelList
            provider={provider}
            models={paged.rows}
            isPending={modelsQuery.isPending}
            isError={modelsQuery.isError}
            onRetry={() => {
              void modelsQuery.refetch()
            }}
            pricing={pricing}
            // Add model sits above the list here, not inside it.
            actions={{ ...modelActions, onCreate: undefined }}
          />
        </div>
        <DataTablePagination pagination={paged.pagination} />
      </section>
      {canManage && (
        <ModelDialogs
          orgId={activeOrg.id}
          providers={[provider]}
          dialog={dialog}
          onClose={() => {
            setDialog(null)
          }}
        />
      )}
    </>
  )
}
