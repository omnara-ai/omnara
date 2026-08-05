import { type SkillListSort, type SkillOwnerScope, useSkills } from '@omnara/react'
import { useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { CreateSkillDialog } from '@/components/org/CreateSkillDialog'
import { SkillRowActions } from '@/components/skills/SkillRowActions'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { formatDateTime } from '@/lib/format'
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
            { header: 'Name' },
            { header: 'Description' },
            { header: 'Revision', className: 'w-24' },
            { header: '', className: 'w-14', isActions: true },
          ]}
          data={paged.rows}
          isFiltered={list.isFiltering}
          pagination={paged.pagination}
          getRowId={(skill) => skill.id}
          rowCells={(skill) => [
            <span className="font-medium">{skill.name}</span>,
            <span className="text-muted-foreground line-clamp-1">{skill.description}</span>,
            <span className="text-muted-foreground tabular-nums">v{skill.revision}</span>,
            <SkillRowActions
              orgId={activeOrg.id}
              skill={skill}
              canDelete={canManage}
              canGrant={canManage && owner.kind !== 'project'}
            />,
          ]}
          rowExpanded={(skill) => (
            <DetailList
              items={[
                { label: 'ID', value: skill.id, mono: true },
                { label: 'Revision ID', value: skill.revision_id, mono: true },
                { label: 'Description', value: skill.description },
                { label: 'Created', value: formatDateTime(skill.created_at) },
                { label: 'Updated', value: formatDateTime(skill.updated_at) },
              ]}
            />
          )}
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
