import { useAgents, useMachine, useServerInfo } from '@omnara/react'
import type { Agent, AgentMcpConnection, AgentProfile } from '@omnara/sdk'
import { type ReactNode, useState } from 'react'

import {
  AgentReferenceLink,
  FetchedAgentReferenceLink,
  ProfileReferenceLink,
} from '@/components/agents/AgentReferenceLinks'
import { AgentScheduledMessages } from '@/components/agents/AgentScheduledMessages'
import { AgentUsageGroup } from '@/components/agents/AgentUsageGroup'
import { CreateCronTriggerDialog } from '@/components/agents/CronTriggerDialog'
import { CronTriggersList } from '@/components/agents/CronTriggersSection'
import { useScheduledMessages } from '@/components/agents/useScheduledMessages'
import { DetailList } from '@/components/data-table/DetailList'
import { InfoIcon, PlusIcon } from '@/components/icons'
import { registryServerLabel } from '@/components/mcp/mcpRegistry'
import { McpServerIcon } from '@/components/mcp/McpServerIcon'
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
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { BudgetCard } from '@/components/usage/BudgetCard'
import { formatDateTime } from '@/lib/format'
import { cn } from '@/lib/utils'

export const sidebarToggleActiveClass =
  'bg-accent text-link hover:bg-primary/15 hover:text-link-hover'

export function AgentSidebarToggle() {
  const { isMobile, open, openMobile, toggleSidebar } = useSidebar()
  const sidebarOpen = isMobile ? openMobile : open
  return (
    <Button
      size="icon"
      variant="ghost"
      aria-label={sidebarOpen ? 'Hide agent details' : 'Show agent details'}
      className={cn('text-muted-foreground', sidebarOpen && sidebarToggleActiveClass)}
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
  canOperate,
  canManageBudget,
}: {
  orgId: string
  projectId: string
  agent: Agent
  machineIds: string[]
  mcpConnections: AgentMcpConnection[]
  profile?: AgentProfile
  canManage: boolean
  canOperate: boolean
  canManageBudget: boolean
}) {
  const [addCronOpen, setAddCronOpen] = useState(false)
  return (
    <>
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
                      <ProfileReferenceLink
                        projectId={projectId}
                        profileId={profile.id}
                        name={profile.name}
                      />
                    ) : undefined,
                  },
                  {
                    label: 'Parent',
                    value: agent.parent_agent_id ? (
                      <FetchedAgentReferenceLink
                        orgId={orgId}
                        projectId={projectId}
                        agentId={agent.parent_agent_id}
                      />
                    ) : undefined,
                  },
                  { label: 'Key', value: agent.subagent_key, mono: true },
                  { label: 'Config', value: agent.current_config_id, mono: true },
                  { label: 'Created', value: formatDateTime(agent.created_at) },
                ]}
              />
            </SidebarGroupContent>
          </SidebarGroup>
          <AgentMachinesGroup orgId={orgId} machineIds={machineIds} />
          <AgentSubagentsGroup orgId={orgId} projectId={projectId} agentId={agent.id} />
          <AgentMcpGroup connections={mcpConnections} />
          <AgentUsageGroup orgId={orgId} projectId={projectId} agentId={agent.id} />
          {canManageBudget && !agent.parent_agent_id && (
            <BudgetCard
              orgId={orgId}
              scope={{ type: 'agent', project_id: projectId, agent_id: agent.id }}
              variant="sidebar"
            />
          )}
          <AgentCronGroup
            orgId={orgId}
            projectId={projectId}
            agentId={agent.id}
            canManage={canManage}
            canOperate={canOperate}
            onAdd={() => {
              setAddCronOpen(true)
            }}
          />
        </SidebarContent>
      </Sidebar>
      {canManage && addCronOpen && (
        <CreateCronTriggerDialog
          open
          onOpenChange={setAddCronOpen}
          orgId={orgId}
          projectId={projectId}
          target={{ type: 'agent', agent_id: agent.id }}
          targetLabel={agent.name || 'this agent'}
        />
      )}
    </>
  )
}

function SidebarEmptyText({ children }: { children: ReactNode }) {
  return <p className="text-muted-foreground truncate py-1.5 text-sm">{children}</p>
}

function AgentMachinesGroup({ orgId, machineIds }: { orgId: string; machineIds: string[] }) {
  return (
    <SidebarGroup>
      <SidebarGroupLabel className="px-0 text-sm">Machines</SidebarGroupLabel>
      <SidebarGroupContent>
        {machineIds.length === 0 ? (
          <SidebarEmptyText>No machines</SidebarEmptyText>
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

function AgentSubagentsGroup({
  orgId,
  projectId,
  agentId,
}: {
  orgId: string
  projectId: string
  agentId: string
}) {
  const query = useAgents(orgId, projectId, {
    filters: { parent_agent_id: agentId, include_archived: true },
    sort: 'created_at',
  })
  const subagents = query.data?.pages.flatMap((page) => page.data) ?? []
  return (
    <SidebarGroup>
      <SidebarGroupLabel className="px-0 text-sm">Subagents</SidebarGroupLabel>
      <SidebarGroupContent>
        {query.isPending ? (
          <SidebarEmptyText>Loading…</SidebarEmptyText>
        ) : subagents.length === 0 ? (
          <SidebarEmptyText>No subagents</SidebarEmptyText>
        ) : (
          <SidebarMenu>
            {subagents.map((subagent) => (
              <SidebarMenuItem
                key={subagent.id}
                className="flex items-center justify-between gap-2 py-1.5 text-sm"
              >
                <span className="flex min-w-0 flex-col">
                  <AgentReferenceLink
                    agent={subagent}
                    name={subagent.name || (subagent.subagent_key ?? 'Agent')}
                  />
                  <span className="text-muted-foreground truncate font-mono text-xs">
                    {subagent.subagent_key}
                  </span>
                </span>
                <Badge variant="outline" className="capitalize">
                  {(subagent.activity?.state ?? subagent.state).replaceAll('_', ' ')}
                </Badge>
              </SidebarMenuItem>
            ))}
            {query.hasNextPage && (
              <SidebarMenuItem className="py-1.5 text-sm">
                <Button
                  variant="link"
                  size="sm"
                  className="h-auto p-0"
                  disabled={query.isFetchingNextPage}
                  onClick={() => void query.fetchNextPage()}
                >
                  Show more
                </Button>
              </SidebarMenuItem>
            )}
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
  agentId,
  canManage,
  canOperate,
  onAdd,
}: {
  orgId: string
  projectId: string
  agentId: string
  canManage: boolean
  canOperate: boolean
  onAdd: () => void
}) {
  const hasScheduledMessages =
    useScheduledMessages({ orgId, projectId, agentId }).scheduled.length > 0
  return (
    <SidebarGroup>
      <div className="flex items-center justify-between">
        <SidebarGroupLabel className="px-0 text-sm">Schedules</SidebarGroupLabel>
        {canManage && (
          <Button
            size="sm"
            variant="ghost"
            className="text-muted-foreground h-9 px-2 sm:h-7"
            onClick={onAdd}
          >
            <PlusIcon />
            Add
          </Button>
        )}
      </div>
      <SidebarGroupContent>
        <AgentScheduledMessages
          orgId={orgId}
          projectId={projectId}
          agentId={agentId}
          canOperate={canOperate}
        />
        <CronTriggersList
          orgId={orgId}
          projectId={projectId}
          canManage={canManage}
          filters={{ agent_id: agentId }}
          emptyMessage="No schedules"
          emptyState={
            hasScheduledMessages ? <></> : <SidebarEmptyText>No schedules</SidebarEmptyText>
          }
        />
      </SidebarGroupContent>
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
          <SidebarEmptyText>No MCP servers</SidebarEmptyText>
        ) : (
          <SidebarMenu>
            {active.map((connection) => (
              <AgentMcpConnectionItem key={connection.server_key} connection={connection} />
            ))}
          </SidebarMenu>
        )}
      </SidebarGroupContent>
    </SidebarGroup>
  )
}

function AgentMcpConnectionItem({ connection }: { connection: AgentMcpConnection }) {
  const info = useServerInfo(connection.endpoint_url)
  const server = info.data ?? null
  const stateLabel =
    connection.state === 'ready'
      ? 'Connected'
      : connection.state === 'initializing'
        ? 'Connecting'
        : connection.state === 'failed'
          ? 'Failed'
          : 'Disconnected'

  return (
    <SidebarMenuItem className="py-1.5 text-sm">
      <Tooltip>
        <TooltipTrigger asChild>
          <div className="flex items-center justify-between gap-2">
            <span className="flex min-w-0 items-center gap-2">
              <McpServerIcon server={server} url={connection.endpoint_url} />
              <span className="truncate">{connection.server_key}</span>
            </span>
            <Badge variant={connection.state === 'failed' ? 'destructive' : 'outline'}>
              {stateLabel}
            </Badge>
          </div>
        </TooltipTrigger>
        <TooltipContent side="left" className="max-w-xs space-y-1 text-left">
          <div className="flex items-center gap-2 font-medium">
            <McpServerIcon server={server} url={connection.endpoint_url} className="size-4" />
            <span className="truncate">
              {server ? registryServerLabel(server) : connection.server_key}
            </span>
          </div>
          {server && <div className="text-muted-foreground break-all">{server.name}</div>}
          {server?.description && <div>{server.description}</div>}
          <div className="text-muted-foreground break-all">{connection.endpoint_url}</div>
          <div className="text-muted-foreground">
            {stateLabel}
            {connection.protocol_version && <span>{` · MCP ${connection.protocol_version}`}</span>}
          </div>
          {connection.initialize_error && (
            <div className="text-destructive break-words">{connection.initialize_error}</div>
          )}
        </TooltipContent>
      </Tooltip>
      {connection.state === 'failed' && connection.initialize_error && (
        <p role="alert" className="text-destructive mt-1 line-clamp-4 text-xs break-words">
          {connection.initialize_error}
        </p>
      )}
    </SidebarMenuItem>
  )
}
