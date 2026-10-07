import {
  useAgentProfiles,
  useClusterModelPricing,
  useConfiguredModels,
  useDeleteProjectModelGrant,
  useModelProviders,
  useProjectModelGrants,
} from '@omnara/react'
import type { ProjectModelGrantListItem } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'

import { AgentCard, AgentCardStat } from '@/components/agents/AgentCardList'
import { OmnaraManagedTag } from '@/components/brand/OmnaraManaged'
import { Box } from '@/components/icons'
import { SectionTitle } from '@/components/layout/SectionTitle'
import { EditModelGrantDialog } from '@/components/projects/EditModelGrantDialog'
import {
  countProfilesByModel,
  groupByProvider,
  profileModelKey,
  type ProviderGroup,
  sharedCountLabel,
} from '@/components/projects/project-model-groups'
import {
  ManageInOrgLink,
  ProjectProviderGlyph,
  ProjectProviderSubtitle,
  ProviderModelRows,
  ShowAvailableToggle,
} from '@/components/projects/ProjectModelsView'
import { Button } from '@/components/ui/button'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { useAllPages } from '@/hooks/use-all-pages'
import { useShareProjectModel } from '@/hooks/use-share-project-model'
import { formatCount } from '@/lib/format'

/**
 * One provider's models in a project, laid out like the org provider page: a header card,
 * then every shared model. Sharing, editing and revoking grants requires project access
 * management.
 */
export function ProjectModelProviderView({
  orgId,
  projectId,
  providerId,
  canManageAccess,
}: {
  orgId: string
  projectId: string
  providerId: string
  canManageAccess: boolean
}) {
  const grants = useAllPages(useProjectModelGrants(orgId, projectId))
  const providers = useAllPages(useModelProviders(orgId))
  const group = groupByProvider(grants.items, providers.items, true).find(
    (candidate) => candidate.id === providerId,
  )

  if (group) {
    return (
      <ProviderPage orgId={orgId} projectId={projectId} group={group} canManage={canManageAccess} />
    )
  }
  if (grants.isPending || providers.isPending) {
    return <Skeleton className="h-[7.25rem] rounded-xl" />
  }
  return (
    <Empty className="rounded-xl border">
      <EmptyHeader>
        <EmptyDescription>
          {grants.isError
            ? 'Couldn’t load this provider.'
            : 'This provider doesn’t exist or isn’t visible to you.'}
        </EmptyDescription>
      </EmptyHeader>
      <EmptyContent>
        <Button asChild size="sm" variant="ghost">
          <Link to="/projects/$projectId/models" params={{ projectId }}>
            Back to models
          </Link>
        </Button>
      </EmptyContent>
    </Empty>
  )
}

function ProviderPage({
  orgId,
  projectId,
  group,
  canManage,
}: {
  orgId: string
  projectId: string
  group: ProviderGroup
  canManage: boolean
}) {
  const { config } = group
  const orgModels = useAllPages(
    useConfiguredModels(orgId, group.id, { enabled: config !== undefined }),
  )
  const profiles = useAllPages(useAgentProfiles(orgId, projectId))
  const pricing = useClusterModelPricing(orgId)
  const deleteGrant = useDeleteProjectModelGrant(orgId, projectId)
  const { sharingId, share } = useShareProjectModel(orgId, projectId)
  const [showAvailable, setShowAvailable] = useState(false)
  const [editing, setEditing] = useState<ProjectModelGrantListItem | null>(null)
  const profileCounts = countProfilesByModel(profiles.items)
  const orgTotal = config && !orgModels.isPending && !orgModels.isError ? orgModels.items.length : 0
  const sharedCount = formatCount(group.items.length)

  return (
    <>
      <AgentCard
        icon={<ProjectProviderGlyph config={config} />}
        title={
          <>
            <h1 className="truncate font-medium">{group.name}</h1>
            {config?.management_kind === 'cluster' && <OmnaraManagedTag />}
          </>
        }
        subtitle={<ProjectProviderSubtitle config={config} />}
        meta={<ManageInOrgLink />}
        stats={
          <AgentCardStat
            icon={Box}
            label={sharedCountLabel(orgTotal, group.items.length)}
            value={orgTotal > 0 ? `${sharedCount} / ${formatCount(orgTotal)}` : sharedCount}
          />
        }
      />
      <section className="flex flex-col gap-3">
        <div className="flex items-center justify-between gap-2">
          <SectionTitle title="Models" />
          {canManage && (
            <ShowAvailableToggle
              pressed={showAvailable}
              onToggle={() => {
                setShowAvailable((value) => !value)
              }}
            />
          )}
        </div>
        <div className="rounded-xl border [&>div]:border-t-0">
          <ProviderModelRows
            projectId={projectId}
            group={group}
            orgModels={orgModels.items}
            orgModelsPending={orgModels.isPending}
            pricing={pricing}
            showAvailable={canManage && showAvailable}
            sharingId={sharingId}
            profileCount={(item) =>
              profileCounts.get(profileModelKey(item.model.provider_config, item.model.name)) ?? 0
            }
            canManage={canManage}
            onEdit={setEditing}
            onStopSharing={(item) => {
              deleteGrant.mutate(item.grant.id)
            }}
            onShare={(modelId) => {
              void share(modelId)
            }}
          />
        </div>
      </section>
      {canManage && editing && (
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
    </>
  )
}
