import {
  type ModelProviderListSort,
  useClusterModelPricing,
  useModelProviders,
} from '@omnara/react'
import type { ModelProviderConfig } from '@omnara/sdk'
import { Link, useNavigate, useSearch } from '@tanstack/react-router'
import { type ReactNode, useId, useState } from 'react'

import {
  AgentCard,
  AgentCardGlyph,
  agentCardLinkClass,
  AgentCardList,
  agentCardMoreLinkClass,
  AgentCardStat,
  AgentCardStatToggle,
  AgentCardTime,
} from '@/components/agents/AgentCardList'
import { ManagedLogo, OmnaraManagedTag } from '@/components/brand/OmnaraManaged'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { Box } from '@/components/icons'
import { SearchHeader } from '@/components/layout/SearchHeader'
import {
  modelProviderKind,
  modelProviderLabel,
} from '@/components/org/CreateModelProviderDialogState'
import { ModelProviderLogo } from '@/components/org/ModelProviderLogo'
import {
  type ModelDialog,
  ModelDialogs,
  ProviderActions,
} from '@/components/overview/ModelManagement'
import {
  type ModelActions,
  type PricingLookup,
  ProviderModelList,
} from '@/components/overview/ProviderModelList'
import { Button } from '@/components/ui/button'
import { useAllPages } from '@/hooks/use-all-pages'
import { useArrayPagination } from '@/hooks/use-array-pagination'
import { useModelActions } from '@/hooks/use-model-actions'
import { useProviderModels } from '@/hooks/use-provider-models'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { guides } from '@/lib/docs'
import { formatCount } from '@/lib/format'
import { clusterFirst } from '@/lib/management-kind'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

const previewLimit = 5

export function ModelProvidersSection() {
  const { activeOrg } = useActiveOrg()
  const canManage = canManageOrg(activeOrg.role)
  const list = useResourceList<ModelProviderListSort>('-created_at')
  const query = useModelProviders(activeOrg.id, { filters: list.apiFilters, sort: list.sort })
  // The API can't order by management kind, so load every provider page and
  // pin cluster-managed providers above the org's own, keeping the server's
  // search and sort within each group.
  const providerPages = useAllPages(query)
  const loadedProviders = clusterFirst(providerPages.items)
  const paged = useArrayPagination(
    providerPages.isPending ? [] : loadedProviders,
    (provider) => provider.id,
  )
  const pricing = useClusterModelPricing(activeOrg.id)
  const [activeDialog, setActiveDialog] = useState<ModelDialog>(null)
  const search = useSearch({ strict: false })
  const navigate = useNavigate()
  const dialog: ModelDialog =
    activeDialog ??
    (search.provider && loadedProviders.length > 0
      ? { kind: 'create-model', providerId: search.provider }
      : null)
  const modelActions = useModelActions(activeOrg.id, canManage, setActiveDialog)

  const newProviderButton = () =>
    canManage ? (
      <Button
        size="sm"
        onClick={() => {
          setActiveDialog({ kind: 'create-provider' })
        }}
      >
        New provider
      </Button>
    ) : undefined

  return (
    <>
      <div id="model-providers" className="flex scroll-mt-6 flex-col gap-3">
        <SearchHeader
          title="Providers"
          description="Connect model providers and configure the models your agents use"
          guide={guides.modelProviders}
          toolbar={
            <ResourceListToolbar
              search={list.search}
              onSearchChange={list.setSearch}
              sort={{ value: list.sort, options: resourceSortOptions, onChange: list.setSort }}
              placeholder="Search providers by name…"
              showSearch
            />
          }
        >
          {newProviderButton()}
        </SearchHeader>
        <AgentCardList
          items={paged.rows}
          getId={(provider) => provider.id}
          renderCard={(provider) => (
            <ProviderCard
              orgId={activeOrg.id}
              provider={provider}
              pricing={pricing}
              modelActions={modelActions}
              defaultExpanded={search.provider === provider.id}
              actions={
                canManage && (
                  <ProviderActions
                    orgId={activeOrg.id}
                    provider={provider}
                    onEdit={() => {
                      setActiveDialog({ kind: 'edit-provider', provider })
                    }}
                  />
                )
              }
            />
          )}
          isFiltered={list.isFiltering}
          pagination={paged.pagination}
          isPending={providerPages.isPending}
          isError={providerPages.isError}
          onRetry={() => {
            void query.refetch()
          }}
          emptyMessage="No model providers yet. Connect OpenAI, OpenRouter, Anthropic, or Amazon Bedrock."
          emptyAction={newProviderButton()}
        />
      </div>
      {canManage && (
        <ModelDialogs
          orgId={activeOrg.id}
          providers={loadedProviders}
          dialog={dialog}
          onClose={() => {
            setActiveDialog(null)
            if (search.provider) void navigate({ to: '/models', search: {}, replace: true })
          }}
        />
      )}
    </>
  )
}

/** Brand tile, name, and subtitle shared by provider cards and the provider page header. */
export function ProviderGlyph({ provider }: { provider: ModelProviderConfig }) {
  return (
    <AgentCardGlyph>
      <ManagedLogo managed={provider.management_kind === 'cluster'}>
        <ModelProviderLogo provider={modelProviderKind(provider)} />
      </ManagedLogo>
    </AgentCardGlyph>
  )
}

export function ProviderSubtitle({ provider }: { provider: ModelProviderConfig }) {
  return (
    <>
      <span className="shrink-0">{modelProviderLabel(modelProviderKind(provider))}</span>
      <span aria-hidden="true">·</span>
      <span className="truncate font-mono">{provider.base_url}</span>
    </>
  )
}

function ProviderCard({
  orgId,
  provider,
  pricing,
  modelActions,
  defaultExpanded,
  actions,
}: {
  orgId: string
  provider: ModelProviderConfig
  pricing: PricingLookup
  modelActions: ModelActions
  defaultExpanded: boolean
  actions: ReactNode
}) {
  const { models, isPending, isError, refetch } = useProviderModels(orgId, provider.id)
  const [expanded, setExpanded] = useState(defaultExpanded)
  const expansionId = useId()
  const countValue = !isPending && !isError ? formatCount(models.length) : undefined
  const countLabel = models.length === 1 ? 'model' : 'models'
  const expandable = models.length > 0 || modelActions.onCreate !== undefined || isError
  const expansion = {
    id: expansionId,
    open: expanded && expandable,
    content: (
      <ProviderModelList
        provider={provider}
        models={models}
        isPending={isPending}
        isError={isError}
        onRetry={refetch}
        pricing={pricing}
        actions={modelActions}
        limit={previewLimit}
        viewAll={
          <Link
            to="/models/providers/$providerId"
            params={{ providerId: provider.id }}
            className={agentCardMoreLinkClass}
          >
            View all {formatCount(models.length)} models →
          </Link>
        }
      />
    ),
  }
  const toggle = () => {
    setExpanded((open) => !open)
  }

  return (
    <AgentCard
      icon={<ProviderGlyph provider={provider} />}
      title={
        <>
          <Link
            to="/models/providers/$providerId"
            params={{ providerId: provider.id }}
            className={agentCardLinkClass}
          >
            {provider.name}
          </Link>
          {provider.management_kind === 'cluster' && <OmnaraManagedTag />}
        </>
      }
      subtitle={<ProviderSubtitle provider={provider} />}
      meta={
        <>
          <AgentCardTime label="Updated" value={provider.updated_at} />
          {actions}
        </>
      }
      stats={
        expandable ? (
          <AgentCardStatToggle
            icon={Box}
            label={countLabel}
            value={countValue}
            expansion={expansion}
            onToggle={toggle}
          />
        ) : (
          <AgentCardStat icon={Box} label={countLabel} value={countValue} />
        )
      }
      expansion={expansion}
    />
  )
}
