import type { MemoryStore, MemoryStoreList, VisibleProject } from '@omnara/sdk'
import type { InfiniteData, UseInfiniteQueryResult } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'

import {
  AgentCard,
  AgentCardGlyph,
  agentCardLinkClass,
  AgentCardList,
  AgentCardTime,
} from '@/components/agents/AgentCardList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { useListToolbarVisibility, type useResourceList } from '@/hooks/use-resource-list'
import { guides } from '@/lib/docs'

export function MemoryStoresSection({
  list,
  query,
  action,
  projectOf,
}: {
  list: ReturnType<typeof useResourceList<'name'>>
  query: UseInfiniteQueryResult<InfiniteData<MemoryStoreList>, unknown>
  action?: ReactNode
  projectOf?: (store: MemoryStore) => VisibleProject | undefined
}) {
  const paged = usePagedQuery(query, list.queryKey)
  const showToolbar = useListToolbarVisibility(list, paged.pagination, query.isSuccess)
  return (
    <div className="flex flex-col gap-3">
      <SearchHeader
        title="Memory"
        description="Configure files your agents keep between conversations and share with other agents."
        guide={guides.memory}
        toolbar={
          <ResourceListToolbar
            search={list.search}
            onSearchChange={list.setSearch}
            placeholder="Search stores by name…"
            showSearch={showToolbar}
          />
        }
      >
        {action}
      </SearchHeader>
      <AgentCardList
        items={paged.rows}
        getId={(store) => store.id}
        renderCard={(store) => <MemoryStoreCard store={store} project={projectOf?.(store)} />}
        pagination={paged.pagination}
        isFiltered={list.isFiltering}
        isPending={query.isPending}
        isError={query.isError}
        onRetry={() => void query.refetch()}
        emptyMessage="No memory stores yet. Create one to share files with your agents."
      />
    </div>
  )
}

function MemoryStoreCard({ store, project }: { store: MemoryStore; project?: VisibleProject }) {
  return (
    <AgentCard
      icon={
        <AgentCardGlyph>
          <span aria-hidden="true" className="text-sm font-medium">
            {store.name.charAt(0).toUpperCase()}
          </span>
        </AgentCardGlyph>
      }
      title={
        <Link
          to="/projects/$projectId/memory/$storeId"
          params={{ projectId: store.project_id, storeId: store.id }}
          className={agentCardLinkClass}
        >
          {store.name}
        </Link>
      }
      subtitle={
        <>
          {project && <span className="truncate">{project.name}</span>}
          {project && <span aria-hidden="true">·</span>}
          <span className="shrink-0">
            {store.agent_access === 'read' ? 'Read-only for agents' : 'Read & write for agents'}
          </span>
        </>
      }
      meta={<AgentCardTime label="Updated" value={store.updated_at} />}
      footer={<span className="truncate">{store.description || 'No description'}</span>}
    />
  )
}
