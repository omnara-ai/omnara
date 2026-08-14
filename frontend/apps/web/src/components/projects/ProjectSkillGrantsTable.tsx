import { type ProjectAvailableSkillListSort, useProjectAvailableSkills } from '@omnara/react'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { SkillRowActions } from '@/components/skills/SkillRowActions'
import { Badge } from '@/components/ui/badge'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { formatDateTime } from '@/lib/format'
import { projectSkillOwnerLabel } from '@/lib/skills'

export function ProjectSkillGrantsTable({
  orgId,
  projectId,
  projectName,
}: {
  orgId: string
  projectId: string
  projectName: string
}) {
  const list = useResourceList<ProjectAvailableSkillListSort>('-updated_at')
  const query = useProjectAvailableSkills(orgId, projectId, {
    filters: { ...list.apiFilters, availability_source: 'grant' },
    sort: list.sort,
  })
  const paged = usePagedQuery(query, list.queryKey)

  return (
    <div className="flex flex-col gap-3">
      <SearchHeader
        title="Skill grants"
        toolbar={
          <ResourceListToolbar
            search={list.search}
            onSearchChange={list.setSearch}
            sort={list.sort}
            sortOptions={resourceSortOptions}
            onSortChange={list.setSort}
            placeholder="Search skill grants by name…"
          />
        }
      />
      <DataTable
        columns={[
          {
            id: 'skill',
            header: 'Skill',
            cell: (access) => <span className="font-medium">{access.skill.name}</span>,
          },
          {
            id: 'description',
            header: 'Description',
            cell: (access) => (
              <span className="text-muted-foreground line-clamp-1">{access.skill.description}</span>
            ),
          },
          {
            id: 'owner',
            header: 'Owner',
            className: 'w-32',
            cell: (access) => <Badge variant="outline">{projectSkillOwnerLabel(access)}</Badge>,
          },
          {
            id: 'actions',
            header: '',
            className: 'w-14',
            isActions: true,
            cell: (access) => (
              <SkillRowActions
                orgId={orgId}
                skill={access.skill}
                availability={access.availability}
                projectName={projectName}
                canDelete
              />
            ),
          },
        ]}
        data={paged.rows}
        isFiltered={list.isFiltering}
        pagination={paged.pagination}
        getRowId={(access) => access.skill.id}
        rowExpanded={({ skill }) => (
          <DetailList
            items={[
              { label: 'ID', value: skill.id, mono: true },
              { label: 'Revision', value: skill.revision },
              { label: 'Description', value: skill.description },
              { label: 'Updated', value: formatDateTime(skill.updated_at) },
            ]}
          />
        )}
        isPending={query.isPending}
        isError={query.isError}
        onRetry={() => {
          void query.refetch()
        }}
        emptyMessage="No skills granted to this project. Grant one from its owner’s Skills page."
      />
    </div>
  )
}
