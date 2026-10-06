import {
  type ProjectModelGrantListSort,
  useAgentProfiles,
  useClusterModelPricing,
  useConfiguredModels,
  useCreateProjectModelGrant,
  useDeleteProjectModelGrant,
  useModelProviders,
  useProjectModelGrants,
} from '@omnara/react'
import {
  type AgentProfileSummary,
  ApiError,
  type ConfiguredModel,
  type ModelProviderConfig,
  type ProjectModelGrantListItem,
} from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { useId, useState } from 'react'

import {
  AgentCard,
  AgentCardGlyph,
  agentCardLinkClass,
  agentCardMoreLinkClass,
  AgentCardStatToggle,
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
import { EditModelGrantDialog } from '@/components/projects/EditModelGrantDialog'
import { GrantModelButton } from '@/components/projects/GrantModelButton'
import { AvailableModelRow, SharedModelRow } from '@/components/projects/ProjectModelRows'
import { Button } from '@/components/ui/button'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { useAllPages } from '@/hooks/use-all-pages'
import { createdResourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { guides } from '@/lib/docs'
import { formatCount } from '@/lib/format'
import { clusterFirst } from '@/lib/management-kind'

type PricingLookup = ReturnType<typeof useClusterModelPricing>

/** Provider a group of shared models belongs to; `config` is absent when the viewer can't list it. */
interface ProviderGroup {
  id: string
  name: string
  config?: ModelProviderConfig
  items: ProjectModelGrantListItem[]
}

function profileModelKey(providerConfig: string, name: string) {
  return `${providerConfig}/${name}`
}

const previewLimit = 5

/**
 * Shared models grouped into provider cards. With `providerId` it's that provider's page:
 * one card listing every model instead of a preview. Anyone who can read the project can
 * view them; sharing, editing and revoking grants requires project access management.
 */
export function ProjectModelsView({
  orgId,
  projectId,
  providerId,
  canManageAccess,
}: {
  orgId: string
  projectId: string
  providerId?: string
  canManageAccess: boolean
}) {
  const list = useResourceList<ProjectModelGrantListSort>('-created_at')
  const grants = useAllPages(
    useProjectModelGrants(orgId, projectId, { filters: list.apiFilters, sort: list.sort }),
  )
  const providers = useAllPages(useModelProviders(orgId))
  const profiles = useAllPages(useAgentProfiles(orgId, projectId))
  const pricing = useClusterModelPricing(orgId)
  const createGrant = useCreateProjectModelGrant(orgId)
  const deleteGrant = useDeleteProjectModelGrant(orgId, projectId)
  const [showAvailable, setShowAvailable] = useState(false)
  const [editing, setEditing] = useState<ProjectModelGrantListItem | null>(null)
  const [sharingId, setSharingId] = useState<string | null>(null)

  const profileCounts = countProfilesByModel(profiles.items)
  const listAvailable = canManageAccess && showAvailable && !list.isFiltering
  const allGroups = groupByProvider(
    grants.items,
    providers.items,
    providerId !== undefined || listAvailable,
  )
  const groups = allGroups.filter((group) => providerId === undefined || group.id === providerId)
  const isPending = grants.isPending || providers.isPending

  async function share(modelId: string) {
    setSharingId(modelId)
    try {
      await createGrant.mutateAsync({ projectID: projectId, configured_model_id: modelId })
    } catch (error) {
      window.alert(error instanceof ApiError ? error.message : 'Could not share model')
    }
    setSharingId(null)
  }

  const isOverview = providerId === undefined

  return (
    <div className="flex flex-col gap-3">
      <SearchHeader
        title={isOverview ? 'Shared models' : 'Models'}
        description={
          isOverview
            ? 'Models agents in this project can use, grouped by provider'
            : 'Models from this provider that agents in this project can use'
        }
        guide={guides.modelProviders}
        toolbar={
          <ResourceListToolbar
            search={list.search}
            onSearchChange={list.setSearch}
            sort={{ value: list.sort, options: createdResourceSortOptions, onChange: list.setSort }}
            placeholder="Search shared models by name…"
            showSearch
          />
        }
      >
        {canManageAccess && (
          <ShowAvailableToggle
            pressed={showAvailable}
            onToggle={() => {
              setShowAvailable((value) => !value)
            }}
          />
        )}
        <GrantModelButton />
      </SearchHeader>
      {isPending ? (
        <div className="flex flex-col gap-5">
          {[0, 1].map((index) => (
            <Skeleton key={index} className="h-[7.25rem] rounded-xl" />
          ))}
        </div>
      ) : grants.isError ? (
        <Empty className="rounded-xl border">
          <EmptyHeader>
            <EmptyDescription>Couldn&rsquo;t load shared models.</EmptyDescription>
          </EmptyHeader>
        </Empty>
      ) : groups.length === 0 ? (
        <SharedModelsEmpty isFiltering={list.isFiltering} canManage={canManageAccess} />
      ) : (
        <ul className="flex flex-col gap-5">
          {groups.map((group) => (
            <li key={group.id}>
              <ProjectProviderCard
                orgId={orgId}
                projectId={projectId}
                group={group}
                limit={isOverview ? previewLimit : undefined}
                pricing={pricing}
                showAvailable={listAvailable}
                sharingId={sharingId}
                profileCount={(item) =>
                  profileCounts.get(profileModelKey(item.model.provider_config, item.model.name)) ??
                  0
                }
                canManage={canManageAccess}
                onEdit={setEditing}
                onStopSharing={(item) => {
                  deleteGrant.mutate(item.grant.id)
                }}
                onShare={(modelId) => {
                  void share(modelId)
                }}
              />
            </li>
          ))}
        </ul>
      )}
      {canManageAccess && editing && (
        <EditModelGrantDialog
          key={editing.grant.id}
          open
          onOpenChange={(open) => {
            if (!open) setEditing(null)
          }}
          orgId={orgId}
          projectId={projectId}
          item={editing}
        />
      )}
    </div>
  )
}

function countProfilesByModel(profiles: AgentProfileSummary[]) {
  const counts = new Map<string, number>()
  for (const profile of profiles) {
    const { name, provider_config: providerConfig } = profile.current_config.model
    const key = profileModelKey(providerConfig, name)
    counts.set(key, (counts.get(key) ?? 0) + 1)
  }
  return counts
}

function ShowAvailableToggle({ pressed, onToggle }: { pressed: boolean; onToggle: () => void }) {
  return (
    <Button size="sm" variant="ghost" aria-pressed={pressed} onClick={onToggle}>
      {pressed ? 'Hide available' : 'Show available'}
    </Button>
  )
}

function SharedModelsEmpty({
  isFiltering,
  canManage,
}: {
  isFiltering: boolean
  canManage: boolean
}) {
  let message = 'No models are shared with this project yet. Ask a project admin to share one.'
  if (isFiltering) message = 'No results.'
  else if (canManage)
    message = 'No shared models. Share an organization model so agents in this project can use it.'
  return (
    <Empty className="rounded-xl border">
      <EmptyHeader>
        <EmptyDescription>{message}</EmptyDescription>
      </EmptyHeader>
      {!isFiltering && (
        <EmptyContent className="flex-row flex-wrap justify-center gap-2">
          <GrantModelButton />
        </EmptyContent>
      )}
    </Empty>
  )
}

function groupByProvider(
  items: ProjectModelGrantListItem[],
  providers: ModelProviderConfig[],
  includeEmpty: boolean,
): ProviderGroup[] {
  const byProvider = new Map<string, ProjectModelGrantListItem[]>()
  for (const item of items) {
    const id = item.model.model_provider_config_id
    byProvider.set(id, [...(byProvider.get(id) ?? []), item])
  }
  const known = clusterFirst(
    [...providers].sort((left, right) => left.name.localeCompare(right.name)),
  )
  const groups: ProviderGroup[] = known
    .filter((config) => includeEmpty || byProvider.has(config.id))
    .map((config) => ({
      id: config.id,
      name: config.name,
      config,
      items: byProvider.get(config.id) ?? [],
    }))
  const knownIds = new Set(known.map((config) => config.id))
  for (const [id, groupItems] of byProvider) {
    if (knownIds.has(id) || !groupItems[0]) continue
    groups.push({ id, name: groupItems[0].model.provider_config, items: groupItems })
  }
  return groups
}

function ProjectProviderCard({
  orgId,
  projectId,
  group,
  limit,
  pricing,
  showAvailable,
  sharingId,
  profileCount,
  canManage,
  onEdit,
  onStopSharing,
  onShare,
}: {
  orgId: string
  projectId: string
  group: ProviderGroup
  /** Preview only this many rows, with a link to the provider's page for the rest. */
  limit?: number
  pricing: PricingLookup
  showAvailable: boolean
  sharingId: string | null
  profileCount: (item: ProjectModelGrantListItem) => number
  canManage: boolean
  onEdit: (item: ProjectModelGrantListItem) => void
  onStopSharing: (item: ProjectModelGrantListItem) => void
  onShare: (modelId: string) => void
}) {
  const { config } = group
  const orgModels = useAllPages(
    useConfiguredModels(orgId, group.id, { enabled: config !== undefined }),
  )
  const [expanded, setExpanded] = useState(true)
  const expansionId = useId()
  const kind = config ? modelProviderKind(config) : 'custom'
  const orgTotal = config && !orgModels.isPending && !orgModels.isError ? orgModels.items.length : 0
  const sharedCount = formatCount(group.items.length)
  const expansion = {
    id: expansionId,
    open: expanded,
    content: (
      <ProviderModelRows
        projectId={projectId}
        group={group}
        orgModels={orgModels.items}
        orgModelsPending={orgModels.isPending}
        limit={limit}
        pricing={pricing}
        showAvailable={showAvailable}
        sharingId={sharingId}
        profileCount={profileCount}
        canManage={canManage}
        onEdit={onEdit}
        onStopSharing={onStopSharing}
        onShare={onShare}
      />
    ),
  }
  const toggle = () => {
    setExpanded((open) => !open)
  }

  return (
    <AgentCard
      icon={
        <AgentCardGlyph>
          <ManagedLogo managed={config?.management_kind === 'cluster'}>
            <ModelProviderLogo provider={kind} />
          </ManagedLogo>
        </AgentCardGlyph>
      }
      title={<ProviderCardTitle projectId={projectId} group={group} linked={limit !== undefined} />}
      subtitle={
        config ? (
          <>
            <span className="shrink-0">{modelProviderLabel(kind)}</span>
            <span aria-hidden="true">·</span>
            <span className="truncate font-mono">{config.base_url}</span>
          </>
        ) : (
          <span className="truncate">Provider details aren&rsquo;t visible to you</span>
        )
      }
      meta={
        <Link
          to="/models"
          className="text-muted-foreground hover:text-foreground relative whitespace-nowrap text-xs hover:underline"
        >
          Manage in org
        </Link>
      }
      stats={
        <AgentCardStatToggle
          icon={Box}
          label={sharedCountLabel(orgTotal, group.items.length)}
          value={orgTotal > 0 ? `${sharedCount} / ${formatCount(orgTotal)}` : sharedCount}
          expansion={expansion}
          onToggle={toggle}
        />
      }
      expansion={expansion}
    />
  )
}

function sharedCountLabel(orgTotal: number, sharedCount: number) {
  if (orgTotal > 0) return 'shared'
  return sharedCount === 1 ? 'model' : 'models'
}

function ProviderCardTitle({
  projectId,
  group,
  linked,
}: {
  projectId: string
  group: ProviderGroup
  linked: boolean
}) {
  return (
    <>
      {linked ? (
        <Link
          to="/projects/$projectId/models/providers/$providerId"
          params={{ projectId, providerId: group.id }}
          className={agentCardLinkClass}
        >
          {group.name}
        </Link>
      ) : (
        <h1 className="truncate font-medium">{group.name}</h1>
      )}
      {group.config?.management_kind === 'cluster' && <OmnaraManagedTag />}
    </>
  )
}

function ProviderModelRows({
  projectId,
  group,
  orgModels,
  orgModelsPending,
  limit,
  pricing,
  showAvailable,
  sharingId,
  profileCount,
  canManage,
  onEdit,
  onStopSharing,
  onShare,
}: {
  projectId: string
  group: ProviderGroup
  orgModels: ConfiguredModel[]
  orgModelsPending: boolean
  limit?: number
  pricing: PricingLookup
  showAvailable: boolean
  sharingId: string | null
  profileCount: (item: ProjectModelGrantListItem) => number
  canManage: boolean
  onEdit: (item: ProjectModelGrantListItem) => void
  onStopSharing: (item: ProjectModelGrantListItem) => void
  onShare: (modelId: string) => void
}) {
  const orgModelById = new Map(orgModels.map((model) => [model.id, model]))
  const sharedIds = new Set(group.items.map((item) => item.grant.configured_model_id))
  const available = showAvailable
    ? clusterFirst(
        orgModels
          .filter((model) => !sharedIds.has(model.id))
          .sort((left, right) => left.name.localeCompare(right.name)),
      )
    : []
  const shownShared = group.items.slice(0, limit)
  const shownAvailable = available.slice(0, limit && Math.max(limit - shownShared.length, 0))
  const hiddenCount =
    group.items.length - shownShared.length + (available.length - shownAvailable.length)

  return (
    <div className="flex flex-col gap-1 border-t px-2 py-2">
      {group.items.length === 0 && available.length === 0 ? (
        <p className="text-muted-foreground px-2 py-1.5 text-sm">
          {orgModelsPending ? 'Loading models…' : 'No models on this provider yet.'}
        </p>
      ) : (
        <ul className="flex flex-col gap-0.5">
          {shownShared.map((item) => (
            <SharedModelRow
              key={item.grant.id}
              item={item}
              orgModel={orgModelById.get(item.grant.configured_model_id)}
              pricing={pricing.pricingFor(
                item.model.model_provider_config_id,
                item.model.provider_model_slug,
              )}
              profileCount={profileCount(item)}
              onEdit={
                canManage
                  ? () => {
                      onEdit(item)
                    }
                  : undefined
              }
              onStopSharing={
                canManage
                  ? () => {
                      if (!window.confirm(`Stop sharing ${item.model.name} with this project?`))
                        return
                      onStopSharing(item)
                    }
                  : undefined
              }
            />
          ))}
          {shownAvailable.length > 0 && (
            <li className="text-muted-foreground px-2 pb-1 pt-2 text-xs font-medium">
              Available to share
            </li>
          )}
          {shownAvailable.map((model) => (
            <AvailableModelRow
              key={model.id}
              model={model}
              pricing={pricing.pricingFor(group.id, model.provider_model_slug)}
              sharing={sharingId === model.id}
              onShare={() => {
                onShare(model.id)
              }}
            />
          ))}
        </ul>
      )}
      {hiddenCount > 0 && (
        <div className="flex justify-end px-1">
          <Link
            to="/projects/$projectId/models/providers/$providerId"
            params={{ projectId, providerId: group.id }}
            className={agentCardMoreLinkClass}
          >
            View all ({formatCount(hiddenCount)} more) →
          </Link>
        </div>
      )}
    </div>
  )
}
