import { type AgentProfileListSort, useAgentProfiles, useDeleteAgentProfile } from '@omnara/react'
import { type AgentProfile, ApiError } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'

import { AgentProfileIntegrations } from '@/components/agents/AgentProfileIntegrations'
import { DeployAgentProfileDialog } from '@/components/agents/DeployAgentProfileDialog'
import { EditAgentProfileDialog } from '@/components/agents/EditAgentProfileDialog'
import { SlackOAuthOutcomeDialog } from '@/components/agents/SlackOAuthOutcomeDialog'
import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { ResourceListToolbar } from '@/components/data-table/ResourceListToolbar'
import { SearchHeader } from '@/components/layout/SearchHeader'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { Button } from '@/components/ui/button'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { resourceSortOptions, useResourceList } from '@/hooks/use-resource-list'
import { formatDateTime } from '@/lib/format'

type ActiveDialog =
  | { kind: 'deploy'; profile: AgentProfile }
  | { kind: 'edit'; profile: AgentProfile }
  | null

export function AgentProfilesSection({
  orgId,
  projectId,
  canManage,
}: {
  orgId: string
  projectId: string
  canManage: boolean
}) {
  const list = useResourceList<AgentProfileListSort>('-updated_at')
  const query = useAgentProfiles(orgId, projectId, {
    filters: list.apiFilters,
    sort: list.sort,
  })
  const paged = usePagedQuery(query, list.queryKey)
  const deleteProfile = useDeleteAgentProfile(orgId, projectId)
  const [activeDialog, setActiveDialog] = useState<ActiveDialog>(null)

  const newProfileButton = () =>
    canManage ? (
      <Button asChild size="sm">
        <Link to="/projects/$projectId/agent-profiles/new" params={{ projectId }}>
          New agent profile
        </Link>
      </Button>
    ) : undefined

  return (
    <>
      <div className="flex flex-col gap-3">
        <SearchHeader
          title="Agent profiles"
          toolbar={
            <ResourceListToolbar
              search={list.search}
              onSearchChange={list.setSearch}
              sort={list.sort}
              sortOptions={resourceSortOptions}
              onSortChange={list.setSort}
              placeholder="Search profiles by name…"
            />
          }
        >
          {newProfileButton()}
        </SearchHeader>
        <DataTable
          columns={[
            {
              id: 'name',
              header: 'Name',
              cell: (profile) => <span className="font-medium">{profile.name}</span>,
            },
            {
              id: 'provider',
              header: 'Provider',
              cell: (profile) => profile.current_config.model.provider_config,
            },
            {
              id: 'model',
              header: 'Model',
              cell: (profile) => (
                <span className="text-muted-foreground">{profile.current_config.model.name}</span>
              ),
            },
            {
              id: 'actions',
              header: '',
              className: 'w-14',
              isActions: true,
              cell: (profile) =>
                canManage ? (
                  <ResourceRowActions
                    onEdit={() => {
                      setActiveDialog({ kind: 'edit', profile })
                    }}
                    onGrant={() => {
                      setActiveDialog({ kind: 'deploy', profile })
                    }}
                    grantLabel="Deploy to app"
                    onDelete={() => {
                      if (!window.confirm(`Delete agent profile ${profile.name}?`)) return
                      deleteProfile.mutate(profile.id, {
                        onError: (error) => {
                          window.alert(
                            error instanceof ApiError
                              ? error.message
                              : 'Could not delete agent profile',
                          )
                        },
                      })
                    }}
                  />
                ) : null,
            },
          ]}
          data={paged.rows}
          isFiltered={list.isFiltering}
          pagination={paged.pagination}
          getRowId={(profile) => profile.id}
          rowExpanded={(profile) => (
            <div className="flex flex-col gap-4">
              <DetailList
                items={[
                  { label: 'ID', value: profile.id, mono: true },
                  { label: 'Generation', value: profile.current_generation },
                  {
                    label: 'Model',
                    value: `${profile.current_config.model.provider_config} · ${profile.current_config.model.name}`,
                  },
                  { label: 'Created', value: formatDateTime(profile.created_at) },
                  { label: 'Updated', value: formatDateTime(profile.updated_at) },
                ]}
              />
              <AgentProfileIntegrations
                orgId={orgId}
                projectId={projectId}
                profileId={profile.id}
                canManage={canManage}
              />
            </div>
          )}
          isPending={query.isPending}
          isError={query.isError}
          onRetry={() => {
            void query.refetch()
          }}
          emptyMessage="No agent profiles yet. Build a reusable config to launch agents from."
        />
      </div>
      {canManage && activeDialog?.kind === 'deploy' && (
        <DeployAgentProfileDialog
          open
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setActiveDialog(null)
          }}
          orgId={orgId}
          projectId={projectId}
          profile={activeDialog.profile}
        />
      )}
      {canManage && activeDialog?.kind === 'edit' && (
        <EditAgentProfileDialog
          open
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setActiveDialog(null)
          }}
          orgId={orgId}
          projectId={projectId}
          profile={activeDialog.profile}
        />
      )}
      <SlackOAuthOutcomeDialog />
    </>
  )
}
