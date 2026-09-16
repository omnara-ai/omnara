import { useAgents, useAgentUsage, useMachine, useServerInfo } from '@omnara/react'
import type { Agent, AgentMcpConnection, AgentProfile, UsageReport } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { type ReactNode, useState } from 'react'

import { CreateCronTriggerDialog } from '@/components/agents/CronTriggerDialog'
import { CronTriggersList } from '@/components/agents/CronTriggersSection'
import { DetailList } from '@/components/data-table/DetailList'
import { ChevronDown, InfoIcon, PlusIcon, UserGroupIcon, UserIcon } from '@/components/icons'
import { registryServerLabel } from '@/components/mcp/mcpRegistry'
import { McpServerIcon } from '@/components/mcp/McpServerIcon'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
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
import { ReportedCost } from '@/components/usage/ReportedCost'
import { formatCompactCount, formatCount, formatDateTime } from '@/lib/format'
import { errorMessage } from '@/lib/submit-status'
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
                  {
                    label: 'Parent',
                    value: agent.parent_agent_id ? (
                      <Link
                        to="/projects/$projectId/agents/$agentId"
                        params={{ projectId, agentId: agent.parent_agent_id }}
                        className="font-mono text-xs hover:underline"
                      >
                        {agent.parent_agent_id}
                      </Link>
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
                  <Link
                    to="/projects/$projectId/agents/$agentId"
                    params={{ projectId, agentId: subagent.id }}
                    className="truncate hover:underline"
                  >
                    {subagent.name || subagent.subagent_key}
                  </Link>
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

function AgentUsageGroup({
  orgId,
  projectId,
  agentId,
}: {
  orgId: string
  projectId: string
  agentId: string
}) {
  const [includeSubagents, setIncludeSubagents] = useState(false)
  const query = useAgentUsage(orgId, projectId, agentId, includeSubagents)
  return (
    <SidebarGroup>
      <div className="flex items-center justify-between gap-2">
        <SidebarGroupLabel className="px-0 text-sm">Usage</SidebarGroupLabel>
        <SubagentsToggle
          checked={includeSubagents}
          onToggle={() => {
            setIncludeSubagents((value) => !value)
          }}
        />
      </div>
      <SidebarGroupContent>
        {query.isPending ? (
          <p className="text-muted-foreground truncate py-1.5 text-sm">Loading…</p>
        ) : query.isError ? (
          <p className="text-destructive py-1.5 text-sm">
            {errorMessage(query.error, 'Could not load usage.')}
          </p>
        ) : query.data.totals.model_calls === 0 ? (
          <p className="text-muted-foreground truncate py-1.5 text-sm">No model usage yet.</p>
        ) : (
          <AgentUsageDetails report={query.data} />
        )}
      </SidebarGroupContent>
    </SidebarGroup>
  )
}

function SubagentsToggle({ checked, onToggle }: { checked: boolean; onToggle: () => void }) {
  const label = checked ? 'Exclude subagents' : 'Include subagents'
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          size="icon"
          variant="ghost"
          aria-pressed={checked}
          aria-label={label}
          className={cn('size-7', checked ? 'text-foreground' : 'text-muted-foreground')}
          onClick={onToggle}
        >
          {checked ? <UserGroupIcon className="size-4" /> : <UserIcon className="size-4" />}
        </Button>
      </TooltipTrigger>
      <TooltipContent side="left">{label}</TooltipContent>
    </Tooltip>
  )
}

function AgentUsageDetails({ report }: { report: UsageReport }) {
  const [expanded, setExpanded] = useState(false)
  return (
    <Collapsible open={expanded} onOpenChange={setExpanded}>
      <CollapsibleTrigger asChild>
        <button
          type="button"
          className="hover:text-foreground flex w-full items-center justify-between gap-2 py-1.5 text-left text-sm"
          aria-label={expanded ? 'Hide usage details' : 'Show usage details'}
        >
          <span className="truncate tabular-nums">
            <ReportedCost modelCalls={report.totals.model_calls} cost={report.totals.cost} />
            <span className="text-muted-foreground">
              {' · '}
              {formatCompactCount(report.totals.tokens.input_tokens_total)} in
              {' · '}
              {formatCompactCount(report.totals.tokens.output_tokens_total)} out
            </span>
          </span>
          <ChevronDown
            className={cn(
              'text-muted-foreground size-4 shrink-0 transition-transform',
              expanded && 'rotate-180',
            )}
          />
        </button>
      </CollapsibleTrigger>
      <CollapsibleContent className="flex flex-col gap-3 pb-1">
        <DetailList
          items={[
            { label: 'Calls', value: formatCount(report.totals.model_calls) },
            {
              label: 'Uncached',
              value: formatCount(report.totals.tokens.uncached_input_tokens),
            },
            {
              label: 'Cache read',
              value: formatCount(report.totals.tokens.cache_read_input_tokens),
            },
            {
              label: 'Cache write',
              value: formatCount(report.totals.tokens.cache_write_input_tokens),
            },
            {
              label: 'Reasoning',
              value: formatCount(report.totals.tokens.reasoning_output_tokens),
            },
          ]}
        />
        {report.by_model.length > 1 && (
          <SidebarMenu>
            {report.by_model.map((row) => (
              <SidebarMenuItem
                key={`${row.model.configured_model_id}:${row.model.provider_model_slug}`}
                className="flex items-center justify-between gap-2 py-1 text-xs"
              >
                <span className="truncate">{row.model.name}</span>
                <span className="text-muted-foreground shrink-0 tabular-nums">
                  {formatCompactCount(row.tokens.input_tokens_total)} in ·{' '}
                  {formatCompactCount(row.tokens.output_tokens_total)} out ·{' '}
                  <ReportedCost modelCalls={row.model_calls} cost={row.cost} />
                </span>
              </SidebarMenuItem>
            ))}
          </SidebarMenu>
        )}
      </CollapsibleContent>
    </Collapsible>
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
          emptyMessage="No schedules"
          emptyState={<SidebarEmptyText>No schedules</SidebarEmptyText>}
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
