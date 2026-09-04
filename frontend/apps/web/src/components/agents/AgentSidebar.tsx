import { useMachine, useServerInfo } from '@omnara/react'
import type { Agent, AgentMcpConnection, AgentProfile } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { useState } from 'react'

import { CreateCronTriggerDialog } from '@/components/agents/CronTriggerDialog'
import { CronTriggersList } from '@/components/agents/CronTriggersSection'
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
import { formatDateTime } from '@/lib/format'
import { cn } from '@/lib/utils'

export const sidebarToggleActiveClass =
  'bg-blue-500/10 text-blue-600 hover:bg-blue-500/15 hover:text-blue-600 dark:text-blue-400 dark:hover:text-blue-400'

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
}: {
  orgId: string
  projectId: string
  agent: Agent
  machineIds: string[]
  mcpConnections: AgentMcpConnection[]
  profile?: AgentProfile
  canManage: boolean
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
          <AgentCronGroup
            orgId={orgId}
            projectId={projectId}
            agentId={agent.id}
            canManage={canManage}
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
  agentId,
  canManage,
  onAdd,
}: {
  orgId: string
  projectId: string
  agentId: string
  canManage: boolean
  onAdd: () => void
}) {
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
        <CronTriggersList
          orgId={orgId}
          projectId={projectId}
          canManage={canManage}
          filters={{ agent_id: agentId }}
          emptyMessage="No schedules attached."
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
          <p className="text-muted-foreground truncate py-1.5 text-sm">No MCP servers connected.</p>
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
            {connection.protocol_version ? ` · MCP ${connection.protocol_version}` : ''}
          </div>
          {connection.initialize_error && (
            <div className="text-destructive break-words">{connection.initialize_error}</div>
          )}
        </TooltipContent>
      </Tooltip>
      {connection.state === 'failed' && connection.initialize_error && (
        <p role="alert" className="text-destructive mt-1 line-clamp-4 break-words text-xs">
          {connection.initialize_error}
        </p>
      )}
    </SidebarMenuItem>
  )
}
