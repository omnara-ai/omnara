import {
  useAgent,
  useAgentChat,
  useAgentInteractions,
  useCancelAgent,
  useCurrentActorId,
  useMe,
  useResolveAgentInteraction,
} from '@omnara/react'
import { useParams } from '@tanstack/react-router'

import { AgentComposer } from '@/components/agents/AgentComposer'
import { AgentConversation } from '@/components/agents/AgentConversation'
import { AgentInteractions } from '@/components/agents/AgentInteractions'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { errorMessage } from '@/lib/submit-status'
import { useActiveOrg } from '@/lib/use-active-org'
import { useProjectPage } from '@/lib/use-project-page'

export function AgentView() {
  const { activeOrg } = useActiveOrg()
  const { project } = useProjectPage()
  const params = useParams({ strict: false })
  const projectId = params.projectId ?? ''
  const agentId = params.agentId ?? ''
  const { data } = useAgent(activeOrg.id, projectId, agentId)
  const agent = data.agent
  const { data: me } = useMe()
  const chat = useAgentChat({ orgID: activeOrg.id, projectID: projectId, agentID: agentId })
  const interactions = useAgentInteractions(activeOrg.id, projectId, agentId, chat.isWorking)
  const resolveInteraction = useResolveAgentInteraction(activeOrg.id, projectId, agentId)
  const cancelAgent = useCancelAgent(activeOrg.id, projectId, agentId)
  const currentActorId = useCurrentActorId(activeOrg.id, projectId, me.user.id)
  const canOperate = project?.access.can_operate ?? false

  async function resolve(
    interactionID: string,
    body: Parameters<typeof resolveInteraction.mutateAsync>[0]['body'],
  ) {
    return resolveInteraction.mutateAsync({ interactionID, body })
  }

  return (
    <div className="mx-auto flex h-[calc(100svh-3rem)] w-full max-w-5xl flex-col overflow-hidden">
      <header className="shrink-0 pb-4">
        <div className="flex min-w-0 flex-col gap-1">
          <PageBreadcrumb
            items={[
              { label: activeOrg.name, to: '/' },
              ...(project ? [{ label: project.name }] : []),
              {
                label: 'Agents',
                to: '/projects/$projectId/agents' as const,
                params: { projectId },
              },
              { label: agent.name || 'Agent' },
            ]}
          />
        </div>
      </header>

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
            interactions.error != null ? errorMessage(interactions.error, 'Unknown error') : null
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
    </div>
  )
}
