import { type ProjectAvailableSkillListSort, useProjectAvailableSkills } from '@omnara/react'
import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'

import { AgentCardList } from '@/components/agents/AgentCardList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
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
import { skillOwnerLabel } from '@/lib/skills'

export function ProjectSharedSkills({
  orgId,
  projectId,
  projectName,
  canManage,
  actions,
}: {
  orgId: string
  projectId: string
  projectName: string
  /** Whether the viewer may stop sharing (requires managing the target project). */
  canManage: boolean
  /** Header controls, e.g. tabs. */
  actions?: ReactNode
}) {
  const list = useResourceList<ProjectAvailableSkillListSort>('-updated_at')
  const query = useProjectAvailableSkills(orgId, projectId, {
    filters: { ...list.apiFilters, availability_source: 'grant' },
    sort: list.sort,
  })
  const paged = usePagedQuery(query, list.queryKey)
  const showToolbar = useListToolbarVisibility(list, paged.pagination, query.isSuccess)

  return (
    <div className="flex flex-col gap-3">
      <SearchHeader
        title="Shared skills"
        description="List of skills accessible to agents in your current project"
        guide={guides.skills}
        toolbar={
          <ResourceListToolbar
            search={list.search}
            onSearchChange={list.setSearch}
            sort={{ value: list.sort, options: resourceSortOptions, onChange: list.setSort }}
            placeholder="Search shared skills by name…"
            showSearch={showToolbar}
          />
        }
      >
        {actions}
      </SearchHeader>
      <AgentCardList
        items={paged.rows}
        getId={(access) => access.skill.id}
        renderCard={(access) => (
          <SkillCard
            skill={access.skill}
            projectId={projectId}
            source={`Shared from ${skillOwnerLabel(access.skill).toLowerCase()}`}
            actions={
              <SkillRowActions
                orgId={orgId}
                skill={access.skill}
                availability={access.availability}
                projectName={projectName}
                canDelete={canManage}
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
        emptyMessage="No skills shared with this project. Share one from its owner’s Skills page."
        emptyAction={
          <Button asChild size="sm" variant="outline">
            <Link to="/skills">Go to skills</Link>
          </Button>
        }
      />
    </div>
  )
}
