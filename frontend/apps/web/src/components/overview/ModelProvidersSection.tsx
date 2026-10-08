import {
  type ModelPricingLookup,
  type ModelProviderListSort,
  useClusterModelPricing,
  useConfiguredModels,
  useModelProvider,
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
import { type ModelActions, ProviderModelList } from '@/components/overview/ProviderModelList'
import { Button } from '@/components/ui/button'
import { useModelActions } from '@/hooks/use-model-actions'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { guides } from '@/lib/docs'
import { formatCount } from '@/lib/format'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

const previewLimit = 5

export function ModelProvidersSection() {
  const { activeOrg } = useActiveOrg()
  const canManage = canManageOrg(activeOrg.role)
  const list = useResourceList<ModelProviderListSort>('-created_at')
  const query = useModelProviders(activeOrg.id, { filters: list.apiFilters, sort: list.sort })
  const paged = usePagedQuery(query, list.queryKey)
  const pricing = useClusterModelPricing(activeOrg.id)
  const [activeDialog, setActiveDialog] = useState<ModelDialog>(null)
  const search = useSearch({ strict: false })
  const navigate = useNavigate()
  // `?provider=` deep-links to adding a model; that provider may not be on the current page.
  // Reading one provider is manage-only, so members skip the lookup.
  const linkedProvider = useModelProvider(
    activeOrg.id,
    canManage ? (search.provider ?? '') : '',
  ).data
  const dialogProviders =
    linkedProvider && !paged.rows.some((provider) => provider.id === linkedProvider.id)
      ? [linkedProvider, ...paged.rows]
      : paged.rows
  const dialog: ModelDialog =
    activeDialog ??
    (linkedProvider ? { kind: 'create-model', providerId: linkedProvider.id } : null)
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
              canManage={canManage}
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
          isPending={query.isPending}
          isError={query.isError}
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
          providers={dialogProviders}
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

interface ProviderCardProps {
  orgId: string
  provider: ModelProviderConfig
  /** Models and the provider page are manage-only, so members see just the provider itself. */
  canManage: boolean
  pricing: ModelPricingLookup
  modelActions: ModelActions
  defaultExpanded: boolean
  actions: ReactNode
}

function ProviderCard({ canManage, ...props }: ProviderCardProps) {
  if (canManage) return <ManagedProviderCard {...props} />
  const { provider, actions } = props
  return (
    <AgentCard
      icon={<ProviderGlyph provider={provider} />}
      title={<ProviderTitle provider={provider} linked={false} />}
      subtitle={<ProviderSubtitle provider={provider} />}
      meta={
        <>
          <AgentCardTime label="Updated" value={provider.updated_at} />
          {actions}
        </>
      }
    />
  )
}

function ProviderTitle({ provider, linked }: { provider: ModelProviderConfig; linked: boolean }) {
  return (
    <>
      {linked ? (
        <Link
          to="/models/providers/$providerId"
          params={{ providerId: provider.id }}
          className={agentCardLinkClass}
        >
          {provider.name}
        </Link>
      ) : (
        <span className="truncate font-medium">{provider.name}</span>
      )}
      {provider.management_kind === 'cluster' && <OmnaraManagedTag />}
    </>
  )
}

/** The first few models of a provider, plus whether more exist and how to label the count. */
function useProviderPreview(orgId: string, providerId: string) {
  // One small page: enough for the preview, and one extra row tells us whether there are more.
  const query = useConfiguredModels(orgId, providerId, { pageSize: previewLimit + 1 })
  const firstPage = query.data?.pages[0]?.data ?? []
  const hasMore = firstPage.length > previewLimit
  const models = firstPage.slice(0, previewLimit)
  const loaded = !query.isPending && !query.isError
  return {
    query,
    models,
    hasMore,
    countValue: loaded ? `${formatCount(models.length)}${hasMore ? '+' : ''}` : undefined,
    countLabel: models.length === 1 && !hasMore ? 'model' : 'models',
  }
}

function ManagedProviderCard({
  orgId,
  provider,
  pricing,
  modelActions,
  defaultExpanded,
  actions,
}: Omit<ProviderCardProps, 'canManage'>) {
  const { query, models, hasMore, countValue, countLabel } = useProviderPreview(orgId, provider.id)
  const [expanded, setExpanded] = useState(defaultExpanded)
  const expansionId = useId()
  const expandable = models.length > 0 || modelActions.onCreate !== undefined || query.isError
  const expansion = {
    id: expansionId,
    open: expanded && expandable,
    content: (
      <ProviderModelList
        provider={provider}
        models={models}
        isPending={query.isPending}
        isError={query.isError}
        onRetry={() => {
          void query.refetch()
        }}
        pricing={pricing}
        actions={modelActions}
        viewAll={
          hasMore && (
            <Link
              to="/models/providers/$providerId"
              params={{ providerId: provider.id }}
              className={agentCardMoreLinkClass}
            >
              View all models →
            </Link>
          )
        }
      />
    ),
  }

  return (
    <AgentCard
      icon={<ProviderGlyph provider={provider} />}
      title={<ProviderTitle provider={provider} linked />}
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
            onToggle={() => {
              setExpanded((open) => !open)
            }}
          />
        ) : (
          <AgentCardStat icon={Box} label={countLabel} value={countValue} />
        )
      }
      expansion={expansion}
    />
  )
}
