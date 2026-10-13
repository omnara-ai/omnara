import { type SkillListSort, type SkillOwnerScope, useSkills } from '@omnara/react'
import { type ReactNode, useState } from 'react'

import { AgentCardList } from '@/components/agents/AgentCardList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { CreateSkillDialog } from '@/components/org/CreateSkillDialog'
import { SkillCard } from '@/components/skills/SkillCard'
import { SkillRowActions } from '@/components/skills/SkillRowActions'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import {
  resourceSortOptions,
  useListToolbarVisibility,
  useResourceList,
} from '@/hooks/use-resource-list'
import { guides } from '@/lib/docs'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function SkillsSection({
  owner = { kind: 'org' },
  canRead: canReadOverride,
  canManage: canManageOverride,
  actions,
}: {
  owner?: SkillOwnerScope
  canRead?: boolean
  canManage?: boolean
  /** Extra header controls shown before the create button, e.g. tabs. */
  actions?: ReactNode
}) {
  const { activeOrg } = useActiveOrg()
  const canManage =
    canManageOverride ??
    (owner.kind === 'user' || (owner.kind === 'org' && canManageOrg(activeOrg.role)))
  const canRead = canReadOverride ?? (owner.kind === 'user' || owner.kind === 'org')

  if (!canRead) {
    return (
      <div className="flex flex-col gap-3">
        {actions && <div className="flex justify-end">{actions}</div>}
        <p className="text-muted-foreground text-sm">
          You don’t have permission to view skills here.
        </p>
      </div>
    )
  }

  return <SkillsList owner={owner} canManage={canManage} actions={actions} />
}

function SkillsList({
  owner,
  canManage,
  actions,
}: {
  owner: SkillOwnerScope
  canManage: boolean
  actions?: ReactNode
}) {
  const { activeOrg } = useActiveOrg()
  const list = useResourceList<SkillListSort>('-updated_at')
  const query = useSkills(activeOrg.id, owner, {
    filters: list.apiFilters,
    sort: list.sort,
  })
  const paged = usePagedQuery(query, list.queryKey)
  const showToolbar = useListToolbarVisibility(list, paged.pagination, query.isSuccess)
  const [open, setOpen] = useState(false)

  const createSkillButton = () =>
    canManage ? (
      <Button
        size="sm"
        onClick={() => {
          setOpen(true)
        }}
      >
        Create skill
      </Button>
    ) : undefined

  return (
    <>
      <div className="flex flex-col gap-3">
        <SearchHeader
          title="Skills"
          description="Configure reusable instructions and scripts your agents can load."
          guide={guides.skills}
          toolbar={
            <ResourceListToolbar
              search={list.search}
              onSearchChange={list.setSearch}
              sort={{ value: list.sort, options: resourceSortOptions, onChange: list.setSort }}
              placeholder="Search skills by name…"
              showSearch={showToolbar}
            />
          }
        >
          {actions}
          {createSkillButton()}
        </SearchHeader>
        <AgentCardList
          items={paged.rows}
          getId={(skill) => skill.id}
          renderCard={(skill) => (
            <SkillCard
              skill={skill}
              projectId={owner.kind === 'project' ? owner.project_id : undefined}
              actions={
                <SkillRowActions
                  orgId={activeOrg.id}
                  skill={skill}
                  canDelete={canManage}
                  canGrant={canManage && owner.kind !== 'project'}
                />
              }
            />
          )}
          pagination={paged.pagination}
          isFiltered={list.isFiltering}
          isPending={query.isPending}
          isError={query.isError}
          onRetry={() => {
            void query.refetch()
          }}
          emptyMessage="No skills yet. Upload a skill folder or archive, or write a SKILL.md."
          emptyAction={createSkillButton()}
        />
      </div>
      {canManage && (
        <CreateSkillDialog open={open} onOpenChange={setOpen} orgId={activeOrg.id} owner={owner} />
      )}
    </>
  )
}
