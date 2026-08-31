import { type SkillListSort, type SkillOwnerScope, useSkills } from '@omnara/react'
import { useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { CreateSkillDialog } from '@/components/org/CreateSkillDialog'
import { SkillDetails } from '@/components/skills/SkillDetails'
import { SkillRowActions } from '@/components/skills/SkillRowActions'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function SkillsSection({
  owner = { kind: 'org' },
  canRead: canReadOverride,
  canManage: canManageOverride,
}: {
  owner?: SkillOwnerScope
  canRead?: boolean
  canManage?: boolean
}) {
  const { activeOrg } = useActiveOrg()
  const canManage =
    canManageOverride ??
    (owner.kind === 'user' || (owner.kind === 'org' && canManageOrg(activeOrg.role)))
  const canRead = canReadOverride ?? (owner.kind === 'user' || owner.kind === 'org')

  if (!canRead) {
    return (
      <p className="text-muted-foreground text-sm">
        You don’t have permission to view skills here.
      </p>
    )
  }

  return <SkillsList owner={owner} canManage={canManage} />
}

function SkillsList({ owner, canManage }: { owner: SkillOwnerScope; canManage: boolean }) {
  const { activeOrg } = useActiveOrg()
  const list = useResourceList<SkillListSort>('-updated_at')
  const query = useSkills(activeOrg.id, owner, {
    filters: list.apiFilters,
    sort: list.sort,
  })
  const paged = usePagedQuery(query, list.queryKey)
  const [open, setOpen] = useState(false)

  return (
    <>
      <div className="flex flex-col gap-3">
        <SearchHeader
          title="Skills"
          toolbar={
            <ResourceListToolbar
              search={list.search}
              onSearchChange={list.setSearch}
              sort={list.sort}
              sortOptions={resourceSortOptions}
              onSortChange={list.setSort}
              placeholder="Search skills by name…"
            />
          }
        >
          {canManage && (
            <Button
              size="sm"
              onClick={() => {
                setOpen(true)
              }}
            >
              Upload skill
            </Button>
          )}
        </SearchHeader>
        <DataTable
          columns={[
            {
              id: 'name',
              header: 'Name',
              cell: (skill) => <span className="font-medium">{skill.name}</span>,
            },
            {
              id: 'description',
              header: 'Description',
              cell: (skill) => (
                <span className="text-muted-foreground line-clamp-1">{skill.description}</span>
              ),
            },
            {
              id: 'revision',
              header: 'Revision',
              className: 'w-24',
              cell: (skill) => (
                <span className="text-muted-foreground tabular-nums">v{skill.revision}</span>
              ),
            },
            {
              id: 'actions',
              header: '',
              className: 'w-14',
              isActions: true,
              cell: (skill) => (
                <SkillRowActions
                  orgId={activeOrg.id}
                  skill={skill}
                  canDelete={canManage}
                  canUpdate={canManage}
                  canGrant={canManage && owner.kind !== 'project'}
                />
              ),
            },
          ]}
          data={paged.rows}
          isFiltered={list.isFiltering}
          pagination={paged.pagination}
          getRowId={(skill) => skill.id}
          rowExpanded={(skill) => <SkillDetails orgId={activeOrg.id} skill={skill} />}
          isPending={query.isPending}
          isError={query.isError}
          onRetry={() => {
            void query.refetch()
          }}
          emptyMessage="No skills yet. Upload an archive containing SKILL.md."
        />
      </div>
      {canManage && (
        <CreateSkillDialog open={open} onOpenChange={setOpen} orgId={activeOrg.id} owner={owner} />
      )}
    </>
  )
}
