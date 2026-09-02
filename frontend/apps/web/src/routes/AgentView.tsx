import {
  useAgent,
  useAgentChat,
  useAgentConfig,
  useAgentInteractions,
  useAgentProfileQuery,
  useCancelAgent,
  useCurrentActorId,
  useMe,
  useResolveAgentInteraction,
} from '@omnara/react'
import { useParams } from '@tanstack/react-router'
import { type CSSProperties, useRef, useState } from 'react'

import { AgentComposer } from '@/components/agents/AgentComposer'
import { AgentConfigPanel, discardConfigEditsPrompt } from '@/components/agents/AgentConfigPanel'
import { AgentConversation } from '@/components/agents/AgentConversation'
import { AgentInputQueue } from '@/components/agents/AgentInputQueue'
import { AgentInteractions } from '@/components/agents/AgentInteractions'
import {
  AgentSidebar,
  AgentSidebarToggle,
  sidebarToggleActiveClass,
} from '@/components/agents/AgentSidebar'
import { hasPendingMcpBuilderOAuthOutcome } from '@/components/agents/pendingMcpBuilderOAuth'
import { SettingsIcon } from '@/components/icons'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { Button } from '@/components/ui/button'
import { MessageScrollerProvider } from '@/components/ui/message-scroller'
import { SidebarProvider } from '@/components/ui/sidebar'
import { errorMessage } from '@/lib/submit-status'
import { useActiveOrg } from '@/lib/use-active-org'
import { useProjectPage } from '@/lib/use-project-page'
import { cn } from '@/lib/utils'

const agentDetailPollInterval = 5_000

export function AgentView() {
  const { activeOrg } = useActiveOrg()
  const { project } = useProjectPage()
  const params = useParams({ strict: false })
  const projectId = params.projectId ?? ''
  const agentId = params.agentId ?? ''
  const chat = useAgentChat({ orgID: activeOrg.id, projectID: projectId, agentID: agentId })
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
  const interactions = useAgentInteractions(activeOrg.id, projectId, agentId, chat.isWorking)
  const resolveInteraction = useResolveAgentInteraction(activeOrg.id, projectId, agentId)
  const cancelAgent = useCancelAgent(activeOrg.id, projectId, agentId)
  const currentActorId = useCurrentActorId(activeOrg.id, projectId, me.user.id)
  const canOperate = project?.access.can_operate ?? false
  const [configOpen, setConfigOpen] = useState(hasPendingMcpBuilderOAuthOutcome)
  const configDirty = useRef(false)
  const canSendNow = canOperate && interactions.data?.data.length === 0

  function closeConfig() {
    configDirty.current = false
    setConfigOpen(false)
  }

  async function resolve(
    interactionID: string,
    body: Parameters<typeof resolveInteraction.mutateAsync>[0]['body'],
  ) {
    return resolveInteraction.mutateAsync({ interactionID, body })
  }

  async function cancelCurrent() {
    const steeringInputIDs: string[] = []
    for (const input of chat.inputBacklog.inputs) {
      if (input.delivery_mode === 'steering') steeringInputIDs.push(input.id)
    }
    const rollback = await chat.inputBacklog.beginCancellation(steeringInputIDs)
    try {
      return await cancelAgent.mutateAsync()
    } catch (error) {
      rollback()
      throw error
    }
  }

  return (
    <SidebarProvider
      className="h-full min-h-0 overflow-hidden"
      style={{ '--sidebar-width': '20rem' } as CSSProperties}
    >
      <MessageScrollerProvider key={agentId} autoScroll>
        <div
          className="mx-auto flex h-full w-full min-w-0 max-w-5xl flex-1 flex-col overflow-hidden"
          style={{ contain: 'paint' }}
        >
          <header className="shrink-0 pb-4">
            <div className="flex min-w-0 items-center justify-between gap-2">
              <PageBreadcrumb
                items={[
                  { id: 'organization', label: activeOrg.name, to: '/' },
                  ...(project ? [{ id: 'project', label: project.name }] : []),
                  {
                    id: 'agents',
                    label: 'Agents',
                    to: '/projects/$projectId/agents' as const,
                    params: { projectId },
                  },
                  { id: 'agent', label: agent.name || 'Agent' },
                ]}
              />
              <div className="flex shrink-0 items-center gap-1">
                {agent.current_config_id !== undefined && (
                  <Button
                    size="icon"
                    variant="ghost"
                    aria-label="Agent configuration"
                    className={cn('text-muted-foreground', configOpen && sidebarToggleActiveClass)}
                    onClick={() => {
                      if (!configOpen) {
                        setConfigOpen(true)
                        return
                      }
                      if (configDirty.current && !window.confirm(discardConfigEditsPrompt)) return
                      closeConfig()
                    }}
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
              <AgentConversation
                chat={chat}
                currentActorId={currentActorId}
                orgID={activeOrg.id}
                projectID={projectId}
                agentID={agentId}
              />
            </main>
          )}

          <div className="mx-auto grid w-full max-w-3xl shrink-0 gap-3 pt-3">
            {!archived && (
              <AgentInteractions
                interactions={interactions.data?.data ?? []}
                pending={resolveInteraction.isPending}
                error={resolveInteraction.error}
                loadError={
                  interactions.error != null
                    ? errorMessage(interactions.error, 'Unknown error')
                    : null
                }
                onResolve={resolve}
                canOperate={canOperate}
              />
            )}
            {archived ? (
              !configOpen && (
                <div className="bg-muted/30 rounded-xl border px-4 py-3 text-center">
                  <p className="text-sm font-medium">This agent is archived</p>
                  <p className="text-muted-foreground mt-1 text-xs">
                    You can view its conversation, but it can no longer receive messages.
                  </p>
                </div>
              )
            ) : (
              <div className={cn('min-w-0', configOpen && 'hidden')}>
                <AgentInputQueue
                  backlog={chat.inputBacklog}
                  canOperate={canOperate}
                  canSendNow={canSendNow}
                />
                {!configOpen && (
                  <AgentComposer
                    chat={chat}
                    model={agentConfig?.model}
                    cancelPending={cancelAgent.isPending}
                    cancelError={cancelAgent.error}
                    onCancel={cancelCurrent}
                    canOperate={canOperate}
                  />
                )}
              </div>
            )}
          </div>
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
      />
    </SidebarProvider>
  )
}
