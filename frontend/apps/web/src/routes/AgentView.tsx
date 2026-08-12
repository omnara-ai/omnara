import {
  useAgent,
  useAgentChat,
  useAgentInteractions,
  useAgentProfileQuery,
  useCancelAgent,
  useCurrentActorId,
  useMe,
  useResolveAgentInteraction,
} from '@omnara/react'
import { useParams } from '@tanstack/react-router'
import { SettingsIcon } from 'lucide-react'
import { type CSSProperties, useState } from 'react'

import { AgentComposer } from '@/components/agents/AgentComposer'
import { AgentConfigPanel } from '@/components/agents/AgentConfigPanel'
import { AgentConversation } from '@/components/agents/AgentConversation'
import { AgentInteractions } from '@/components/agents/AgentInteractions'
import {
  AgentSidebar,
  AgentSidebarToggle,
  sidebarToggleActiveClass,
} from '@/components/agents/AgentSidebar'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { Button } from '@/components/ui/button'
import { SidebarProvider } from '@/components/ui/sidebar'
import { errorMessage } from '@/lib/submit-status'
import { useActiveOrg } from '@/lib/use-active-org'
import { useProjectPage } from '@/lib/use-project-page'
import { cn } from '@/lib/utils'

export function AgentView() {
  const { activeOrg } = useActiveOrg()
  const { project } = useProjectPage()
  const params = useParams({ strict: false })
  const projectId = params.projectId ?? ''
  const agentId = params.agentId ?? ''
  const { data } = useAgent(activeOrg.id, projectId, agentId)
  const agent = data.agent
  const { data: profile } = useAgentProfileQuery(activeOrg.id, projectId, agent.agent_profile_id)
  const { data: me } = useMe()
  const chat = useAgentChat({ orgID: activeOrg.id, projectID: projectId, agentID: agentId })
  const interactions = useAgentInteractions(activeOrg.id, projectId, agentId, chat.isWorking)
  const resolveInteraction = useResolveAgentInteraction(activeOrg.id, projectId, agentId)
  const cancelAgent = useCancelAgent(activeOrg.id, projectId, agentId)
  const currentActorId = useCurrentActorId(activeOrg.id, projectId, me.user.id)
  const canOperate = project?.access.can_operate ?? false
  const [configOpen, setConfigOpen] = useState(false)

  async function resolve(
    interactionID: string,
    body: Parameters<typeof resolveInteraction.mutateAsync>[0]['body'],
  ) {
    return resolveInteraction.mutateAsync({ interactionID, body })
  }

  return (
    <SidebarProvider
      className="h-[calc(100svh-3rem)] min-h-0 overflow-hidden"
      style={{ '--sidebar-width': '20rem' } as CSSProperties}
    >
      <div className="mx-auto flex h-full w-full min-w-0 max-w-5xl flex-1 flex-col overflow-hidden">
        <header className="shrink-0 pb-4">
          <div className="flex min-w-0 items-center justify-between gap-2">
            <PageBreadcrumb
              items={[
                { label: activeOrg.name, to: '/' },
                ...(project ? [{ label: project.name }] : []),
                {
                  label: 'Agents',
                  to: '/projects/$projectId/agents' as const,
                  params: { projectId },
                },
                ...(profile
                  ? [
                      {
                        label: profile.name,
                        to: '/projects/$projectId/agent-profiles/$profileId' as const,
                        params: { projectId, profileId: profile.id },
                      },
                    ]
                  : []),
                { label: agent.name || 'Agent' },
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
                    setConfigOpen((open) => !open)
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
              onClose={() => {
                setConfigOpen(false)
              }}
            />
          </main>
        ) : (
          <>
            <main className="min-h-0 flex-1">
              <AgentConversation
                chat={chat}
                currentActorId={currentActorId}
                orgID={activeOrg.id}
                projectID={projectId}
              />
            </main>

            <div className="mx-auto grid w-full max-w-3xl shrink-0 gap-3">
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
              <AgentComposer
                chat={chat}
                cancelPending={cancelAgent.isPending}
                cancelError={cancelAgent.error}
                onCancel={() => cancelAgent.mutateAsync()}
                canOperate={canOperate}
              />
            </div>
          </>
        )}
      </div>
      <AgentSidebar
        orgId={activeOrg.id}
        projectId={projectId}
        agent={agent}
        machineBindings={data.machine_bindings}
        mcpConnections={data.mcp_connections}
        profile={profile}
      />
    </SidebarProvider>
  )
}
