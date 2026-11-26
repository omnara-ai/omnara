import {
  isAgentActive,
  useAgent,
  useAgentChat,
  useAgentConfig,
  useAgentInteractions,
  useAgentProfileQuery,
  useCancelAgent,
  useCurrentActorId,
  useMe,
} from '@omnara/react'
import { useMatchRoute, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { type ComponentProps, useRef, useState } from 'react'

import { agentPaneWidth } from '@/components/agents/agent-pane'
import { AgentComposer } from '@/components/agents/AgentComposer'
import { AgentConfigPanel, discardConfigEditsPrompt } from '@/components/agents/AgentConfigPanel'
import { AgentConversation } from '@/components/agents/AgentConversation'
import { AgentEventLog } from '@/components/agents/AgentEventLog'
import { AgentIcon } from '@/components/agents/AgentIcon'
import { AgentInputQueue } from '@/components/agents/AgentInputQueue'
import { AgentInteractions } from '@/components/agents/AgentInteractions'
import {
  AgentSidebar,
  AgentSidebarToggle,
  sidebarToggleActiveClass,
} from '@/components/agents/AgentSidebar'
import { cancelAgentChatTurn } from '@/components/agents/cancel-agent-chat-turn'
import { hasPendingMcpBuilderOAuthOutcome } from '@/components/agents/pendingMcpBuilderOAuth'
import { PillTabs } from '@/components/agents/PillTabs'
import { AgentTraces } from '@/components/agents/traces/AgentTraces'
import { SettingsIcon } from '@/components/icons'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { Button } from '@/components/ui/button'
import { MessageScrollerProvider } from '@/components/ui/message-scroller'
import { SidebarProvider } from '@/components/ui/sidebar'
import { agentIcon, profileIcon } from '@/lib/agent-icon'
import type { CssVariables } from '@/lib/css'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'
import { useProjectPage } from '@/lib/use-project-page'
import { cn } from '@/lib/utils'

const agentDetailPollInterval = 5_000
const agentSidebarStyle: CssVariables = { '--sidebar-width': agentPaneWidth }

type AgentViewMode = 'events' | 'chat' | 'traces'

const viewTabs: { value: AgentViewMode; label: string }[] = [
  { value: 'events', label: 'Event log' },
  { value: 'chat', label: 'Chat' },
  { value: 'traces', label: 'Traces' },
]
const viewPaths = {
  events: '/projects/$projectId/agents/$agentId/events',
  chat: '/projects/$projectId/agents/$agentId/chat',
  traces: '/projects/$projectId/agents/$agentId/traces',
} as const

function useAgentViewMode(): AgentViewMode {
  const matchRoute = useMatchRoute()
  if (matchRoute({ to: viewPaths.chat })) return 'chat'
  if (matchRoute({ to: viewPaths.traces })) return 'traces'
  return 'events'
}

/** The agent's event log, chat or traces, whichever view the route names. */
function AgentViewBody({
  view,
  chat,
  currentActorId,
  orgId,
  projectId,
  agentId,
  tracesToolbar,
  initialTurnId,
  initialSpanId,
}: {
  view: AgentViewMode
  chat: ReturnType<typeof useAgentChat>
  currentActorId: ReturnType<typeof useCurrentActorId>
  orgId: string
  projectId: string
  agentId: string
  tracesToolbar: HTMLElement | null
  initialTurnId?: string
  initialSpanId?: string
}) {
  if (view === 'events')
    return <AgentEventLog chat={chat} orgId={orgId} projectId={projectId} agentId={agentId} />
  if (view === 'traces')
    return (
      <AgentTraces
        orgID={orgId}
        projectID={projectId}
        agentID={agentId}
        toolbar={tracesToolbar}
        initialTurnId={initialTurnId}
        initialSpanId={initialSpanId}
      />
    )
  return (
    <AgentConversation
      chat={chat}
      currentActorId={currentActorId}
      orgID={orgId}
      projectID={projectId}
      agentID={agentId}
    />
  )
}

export function AgentView() {
  const navigate = useNavigate()
  const view = useAgentViewMode()
  const { activeOrg } = useActiveOrg()
  const { project } = useProjectPage()
  const params = useParams({ strict: false })
  const projectId = params.projectId ?? ''
  const agentId = params.agentId ?? ''
  const chat = useAgentChat(
    { orgID: activeOrg.id, projectID: projectId, agentID: agentId },
    { source: 'web' },
  )
  const { data } = useAgent(activeOrg.id, projectId, agentId, {
    refetchInterval: (detail) =>
      chat.isWorking ||
      detail?.mcp_connections.some((connection) => connection.state === 'initializing')
        ? agentDetailPollInterval
        : false,
  })
  const agent = data.agent
  const { data: agentConfig } = useAgentConfig(activeOrg.id, projectId, agent.current_config_id)
  const archived = agent.state === 'archived'
  const { data: profile } = useAgentProfileQuery(activeOrg.id, projectId, agent.agent_profile_id)
  const { data: me } = useMe()
  const interactions = useAgentInteractions(activeOrg.id, projectId, agentId)
  const cancelAgent = useCancelAgent(activeOrg.id, projectId, agentId)
  const currentActorId = useCurrentActorId(activeOrg.id, projectId, me.user.id)
  const canOperate = project?.access.can_operate ?? false
  const search = useSearch({ strict: false })
  // The traces view puts its turn picker here, in line with the view tabs.
  const [tracesToolbar, setTracesToolbar] = useState<HTMLDivElement | null>(null)
  const [configOpen, setConfigOpen] = useState(
    () => search.config === true || hasPendingMcpBuilderOAuthOutcome(),
  )
  const configDirty = useRef(false)
  const canSendNow =
    canOperate &&
    interactions.data != null &&
    !interactions.data.data.some((interaction) => interaction.agent_id === agentId)

  function closeConfig() {
    configDirty.current = false
    setConfigOpen(false)
    if (search.config) {
      void navigate({
        to: viewPaths[view],
        params: { projectId, agentId },
        search: {},
        replace: true,
      })
    }
  }

  function toggleConfig() {
    if (!configOpen) {
      setConfigOpen(true)
      return
    }
    if (configDirty.current && !window.confirm(discardConfigEditsPrompt)) return
    closeConfig()
  }

  return (
    <SidebarProvider className="h-full min-h-0 overflow-hidden" style={agentSidebarStyle}>
      <MessageScrollerProvider key={agentId} autoScroll>
        <div
          className={cn(
            'mx-auto flex h-full w-full min-w-0 flex-1 flex-col overflow-hidden',
            // A trace needs the room for its waterfall and attribute values.
            view === 'traces' && !configOpen ? 'max-w-7xl px-4' : 'max-w-5xl',
          )}
          style={{ contain: 'paint' }}
        >
          <header className="shrink-0 pb-4">
            <div className="flex min-w-0 items-center justify-between gap-2">
              <PageBreadcrumb
                items={[
                  {
                    id: 'agents',
                    label: 'Agents',
                    to: '/projects/$projectId/agents' as const,
                    params: { projectId },
                  },
                  ...(profile
                    ? [
                        {
                          id: 'profile',
                          label: profile.name,
                          to: '/projects/$projectId/agent-profiles/$profileId' as const,
                          params: { projectId, profileId: profile.id },
                          icon: (
                            <AgentIcon
                              icon={profileIcon(profile.id)}
                              className="size-4 rounded-[2px]"
                            />
                          ),
                        },
                      ]
                    : []),
                  {
                    id: 'agent',
                    label: agent.name || 'Agent',
                    icon: (
                      <AgentIcon
                        icon={agentIcon(agent.agent_profile_id, agent.id)}
                        animated={isAgentActive(agent)}
                        className="size-4 rounded-[2px]"
                      />
                    ),
                  },
                ]}
              />
              {/* Padded so the picker's focus ring is not clipped by the page column. */}
              <div ref={setTracesToolbar} className="flex min-w-0 items-center gap-3 py-1" />
              <div className="ml-auto flex shrink-0 items-center gap-2">
                <AgentViewTabs
                  hidden={configOpen}
                  view={view}
                  projectId={projectId}
                  agentId={agentId}
                />
                {agent.current_config_id !== undefined && (
                  <Button
                    size="icon"
                    variant="ghost"
                    aria-label="Agent configuration"
                    className={cn('text-muted-foreground', configOpen && sidebarToggleActiveClass)}
                    onClick={toggleConfig}
                  >
                    <SettingsIcon />
                  </Button>
                )}
                <AgentSidebarToggle />
              </div>
            </div>
          </header>

          {configOpen ? (
            <main className="min-h-0 flex-1 overflow-y-auto">
              <AgentConfigPanel
                orgId={activeOrg.id}
                projectId={projectId}
                agent={agent}
                canManage={project?.access.can_manage ?? false}
                onDirtyChange={(dirty) => {
                  configDirty.current = dirty
                }}
                onClose={closeConfig}
              />
            </main>
          ) : (
            <main className="min-h-0 flex-1">
              <AgentViewBody
                view={view}
                chat={chat}
                currentActorId={currentActorId}
                orgId={activeOrg.id}
                projectId={projectId}
                agentId={agentId}
                tracesToolbar={tracesToolbar}
                initialTurnId={search.turn}
                initialSpanId={search.span}
              />
            </main>
          )}

          <AgentDock
            archived={archived}
            configOpen={configOpen}
            composer={view === 'chat'}
            chat={chat}
            model={agentConfig?.model}
            canOperate={canOperate}
            canSendNow={canSendNow}
            orgID={activeOrg.id}
            projectID={projectId}
            agentID={agentId}
            cancelPending={cancelAgent.isPending}
            cancelError={cancelAgent.error}
            onCancel={() => cancelAgentChatTurn(chat, () => cancelAgent.mutateAsync())}
          />
        </div>
      </MessageScrollerProvider>
      <AgentSidebar
        orgId={activeOrg.id}
        projectId={projectId}
        agent={agent}
        machineIds={data.machine_ids}
        mcpConnections={data.mcp_connections}
        profile={profile}
        canManage={project?.access.can_manage ?? false}
        canOperate={canOperate}
        canManageBudget={canManageOrg(activeOrg.role)}
      />
    </SidebarProvider>
  )
}

function AgentViewTabs({
  hidden,
  view,
  projectId,
  agentId,
}: {
  hidden: boolean
  view: AgentViewMode
  projectId: string
  agentId: string
}) {
  const navigate = useNavigate()
  if (hidden) return null
  return (
    <PillTabs
      value={view}
      tabs={viewTabs}
      onValueChange={(nextView) => {
        void navigate({
          to: viewPaths[nextView],
          params: { projectId, agentId },
        })
      }}
    />
  )
}

function AgentDock({
  archived,
  configOpen,
  composer,
  chat,
  model,
  canOperate,
  canSendNow,
  orgID,
  projectID,
  agentID,
  cancelPending,
  cancelError,
  onCancel,
}: {
  archived: boolean
  configOpen: boolean
  composer: boolean
  chat: ReturnType<typeof useAgentChat>
  model: ComponentProps<typeof AgentComposer>['model']
  canOperate: boolean
  canSendNow: boolean
  orgID: string
  projectID: string
  agentID: string
  cancelPending: boolean
  cancelError: Error | null
  onCancel: () => Promise<void>
}) {
  const composerHidden = !composer || configOpen
  return (
    <div
      className={cn(
        'mx-auto grid w-full max-w-3xl shrink-0 gap-3 pt-3',
        !configOpen && 'pinned-actions',
      )}
    >
      {!archived && !configOpen && (
        <AgentInteractions
          orgID={orgID}
          projectID={projectID}
          agentID={agentID}
          canOperate={canOperate}
        />
      )}
      {archived ? (
        composer &&
        !configOpen && (
          <div className="bg-muted/30 rounded-xl border px-4 py-3 text-center">
            <p className="text-sm font-medium">This agent is archived</p>
            <p className="text-muted-foreground mt-1 text-xs">
              You can view its conversation, but it can no longer receive messages.
            </p>
          </div>
        )
      ) : (
        <div className={cn('min-w-0', composerHidden && 'hidden')}>
          <AgentInputQueue
            backlog={chat.inputBacklog}
            canOperate={canOperate}
            canSendNow={canSendNow}
          />
          <AgentComposer
            chat={chat}
            model={model}
            cancelPending={cancelPending}
            cancelError={cancelError}
            onCancel={onCancel}
            canOperate={canOperate}
            hidden={composerHidden}
          />
        </div>
      )}
    </div>
  )
}
