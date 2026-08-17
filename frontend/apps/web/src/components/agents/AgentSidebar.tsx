import { useMachine } from '@omnara/react'
import type { Agent, AgentMcpConnection, AgentProfile } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { InfoIcon } from 'lucide-react'

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
}: {
  orgId: string
  projectId: string
  agent: Agent
  machineIds: string[]
  mcpConnections: AgentMcpConnection[]
  profile?: AgentProfile
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
