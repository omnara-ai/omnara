import {
  useCronTriggers,
  useDeleteCronTrigger,
  useMachine,
  useUpdateCronTrigger,
} from '@omnara/react'
import { type Agent, type AgentMcpConnection, type AgentProfile, ApiError } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { InfoIcon, PlusIcon, Trash2 } from 'lucide-react'
import { useState } from 'react'

import { CreateCronTriggerDialog } from '@/components/agents/CronTriggersSection'
import { DetailList } from '@/components/data-table/DetailList'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuItem,
  useSidebar,
} from '@/components/ui/sidebar'
import { Spinner } from '@/components/ui/spinner'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { formatDateTime } from '@/lib/format'
import { cn } from '@/lib/utils'

export const sidebarToggleActiveClass =
  'bg-blue-500/10 text-blue-600 hover:bg-blue-500/15 hover:text-blue-600 dark:text-blue-400 dark:hover:text-blue-400'

export function AgentSidebarToggle() {
  const { open, toggleSidebar } = useSidebar()
  return (
    <Button
      size="icon"
      variant="ghost"
      aria-label={open ? 'Hide agent details' : 'Show agent details'}
      className={cn('text-muted-foreground', open && sidebarToggleActiveClass)}
      onClick={toggleSidebar}
    >
      <InfoIcon />
    </Button>
  )
}

export function AgentSidebar({
  orgId,
  projectId,
  agent,
  machineIds,
  mcpConnections,
  profile,
  canManage,
}: {
  orgId: string
  projectId: string
  agent: Agent
  machineIds: string[]
  mcpConnections: AgentMcpConnection[]
  profile?: AgentProfile
  canManage: boolean
}) {
  return (
    <Sidebar side="right" collapsible="offcanvas">
      <SidebarContent className="px-3 pt-4">
        <SidebarGroup>
          <SidebarGroupContent>
            <DetailList
              items={[
                {
                  label: 'Model',
                  value: agent.model
                    ? `${agent.model.name} · ${agent.model.provider_config}`
                    : undefined,
                },
                {
                  label: 'Profile',
                  value: profile ? (
                    <Link
                      to="/projects/$projectId/agent-profiles/$profileId"
                      params={{ projectId, profileId: profile.id }}
                      className="hover:underline"
                    >
                      {profile.name}
                    </Link>
                  ) : undefined,
                },
                { label: 'Config', value: agent.current_config_id, mono: true },
                { label: 'Created', value: formatDateTime(agent.created_at) },
              ]}
            />
          </SidebarGroupContent>
        </SidebarGroup>
        <AgentMachinesGroup orgId={orgId} machineIds={machineIds} />
        <AgentMcpGroup connections={mcpConnections} />
        <AgentCronGroup orgId={orgId} projectId={projectId} agent={agent} canManage={canManage} />
      </SidebarContent>
    </Sidebar>
  )
}

function AgentMachinesGroup({ orgId, machineIds }: { orgId: string; machineIds: string[] }) {
  return (
    <SidebarGroup>
      <SidebarGroupLabel className="px-0 text-sm">Machines</SidebarGroupLabel>
      <SidebarGroupContent>
        {machineIds.length === 0 ? (
          <p className="text-muted-foreground truncate py-1.5 text-sm">No machines attached.</p>
        ) : (
          <SidebarMenu>
            {machineIds.map((machineId) => (
              <AgentMachineRow key={machineId} orgId={orgId} machineId={machineId} />
            ))}
          </SidebarMenu>
        )}
      </SidebarGroupContent>
    </SidebarGroup>
  )
}

const provisioningPollInterval = 5_000
const connectionStatePollInterval = 15_000

function AgentMachineRow({ orgId, machineId }: { orgId: string; machineId: string }) {
  const { data: machine } = useMachine(orgId, machineId, {
    refetchInterval: (machine) =>
      machine == null ? provisioningPollInterval : connectionStatePollInterval,
  })
  return (
    <SidebarMenuItem className="flex items-center justify-between gap-2 py-1.5 text-sm">
      <span className="truncate">{machine?.display_name ?? machineId}</span>
      {machine && (
        <Badge variant="outline" className="capitalize">
          {machine.connection_state}
        </Badge>
      )}
    </SidebarMenuItem>
  )
}

function AgentCronGroup({
  orgId,
  projectId,
  agent,
  canManage,
}: {
  orgId: string
  projectId: string
  agent: Agent
  canManage: boolean
}) {
  const query = useCronTriggers(orgId, projectId, { filters: { agent_id: agent.id } })
  const triggers = useInfiniteQueryItems(query)
  const updateTrigger = useUpdateCronTrigger(orgId, projectId)
  const deleteTrigger = useDeleteCronTrigger(orgId, projectId)
  const [addOpen, setAddOpen] = useState(false)

  return (
    <SidebarGroup>
      <div className="flex items-center justify-between">
        <SidebarGroupLabel className="px-0 text-sm">Schedules</SidebarGroupLabel>
        {canManage && (
          <Button
            size="sm"
            variant="ghost"
            className="text-muted-foreground h-7 px-2"
            onClick={() => {
              setAddOpen(true)
            }}
          >
            <PlusIcon />
            Add
          </Button>
        )}
      </div>
      <SidebarGroupContent>
        {query.isPending ? (
          <Spinner className="my-1.5 size-4" />
        ) : query.isError ? (
          <div className="flex items-center justify-between gap-2 py-1.5">
            <p className="text-muted-foreground truncate text-sm">Couldn&rsquo;t load schedules.</p>
            <Button
              size="sm"
              variant="outline"
              className="h-7"
              onClick={() => {
                void query.refetch()
              }}
            >
              Retry
            </Button>
          </div>
        ) : triggers.length === 0 ? (
          <p className="text-muted-foreground truncate py-1.5 text-sm">No schedules attached.</p>
        ) : (
          <SidebarMenu>
            {triggers.map((trigger) => (
              <SidebarMenuItem
                key={trigger.id}
                className="flex items-center justify-between gap-2 py-1.5 text-sm"
              >
                <span className="min-w-0">
                  <span className="block truncate">{trigger.name}</span>
                  <span className="text-muted-foreground block truncate font-mono text-xs">
                    {trigger.cron}
                  </span>
                </span>
                <span className="flex shrink-0 items-center gap-1">
                  {canManage ? (
                    <button
                      type="button"
                      aria-label={`${trigger.enabled ? 'Disable' : 'Enable'} schedule ${trigger.name}`}
                      title={trigger.enabled ? 'Disable schedule' : 'Enable schedule'}
                      disabled={updateTrigger.isPending}
                      onClick={() => {
                        updateTrigger.mutate(
                          { cronTriggerID: trigger.id, enabled: !trigger.enabled },
                          {
                            onError: (error) => {
                              window.alert(
                                error instanceof ApiError
                                  ? error.message
                                  : 'Could not update schedule',
                              )
                            },
                          },
                        )
                      }}
                    >
                      <Badge variant="outline" className="hover:bg-accent cursor-pointer">
                        {trigger.enabled ? 'Enabled' : 'Disabled'}
                      </Badge>
                    </button>
                  ) : (
                    <Badge variant="outline">{trigger.enabled ? 'Enabled' : 'Disabled'}</Badge>
                  )}
                  {canManage && (
                    <Button
                      size="icon"
                      variant="ghost"
                      aria-label={`Delete schedule ${trigger.name}`}
                      className="text-muted-foreground size-7"
                      disabled={deleteTrigger.isPending}
                      onClick={() => {
                        if (!window.confirm(`Delete schedule ${trigger.name}?`)) return
                        deleteTrigger.mutate(trigger.id, {
                          onError: (error) => {
                            window.alert(
                              error instanceof ApiError
                                ? error.message
                                : 'Could not delete schedule',
                            )
                          },
                        })
                      }}
                    >
                      <Trash2 />
                    </Button>
                  )}
                </span>
              </SidebarMenuItem>
            ))}
          </SidebarMenu>
        )}
        {query.hasNextPage && (
          <Button
            size="sm"
            variant="outline"
            className="mt-1 h-7"
            disabled={query.isFetchingNextPage}
            onClick={() => {
              void query.fetchNextPage()
            }}
          >
            Show more
          </Button>
        )}
      </SidebarGroupContent>
      {canManage && addOpen && (
        <CreateCronTriggerDialog
          open
          onOpenChange={setAddOpen}
          orgId={orgId}
          projectId={projectId}
          target={{ type: 'agent', agent_id: agent.id }}
          targetLabel={agent.name || 'this agent'}
        />
      )}
    </SidebarGroup>
  )
}

function AgentMcpGroup({ connections }: { connections: AgentMcpConnection[] }) {
  const active = connections.filter((connection) => connection.state !== 'expired')

  return (
    <SidebarGroup>
      <SidebarGroupLabel className="px-0 text-sm">MCP servers</SidebarGroupLabel>
      <SidebarGroupContent>
        {active.length === 0 ? (
          <p className="text-muted-foreground truncate py-1.5 text-sm">No MCP servers connected.</p>
        ) : (
          <SidebarMenu>
            {active.map((connection) => (
              <SidebarMenuItem
                key={connection.server_key}
                className="flex items-center justify-between gap-2 py-1.5 text-sm"
              >
                <span className="truncate">{connection.server_key}</span>
                <Badge variant="outline">
                  {connection.state === 'ready'
                    ? 'Connected'
                    : connection.state === 'initializing'
                      ? 'Connecting'
                      : 'Disconnected'}
                </Badge>
              </SidebarMenuItem>
            ))}
          </SidebarMenu>
        )}
      </SidebarGroupContent>
    </SidebarGroup>
  )
}
