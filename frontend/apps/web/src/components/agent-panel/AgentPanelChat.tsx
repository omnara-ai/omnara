import {
  useAgentChat,
  useAgentConfig,
  useAgentInteractions,
  useAgentQuery,
  useCancelAgent,
  useCurrentActorId,
  useMe,
} from '@omnara/react'
import type { AgentPanelSelection } from '@omnara/sdk'

import { AgentComposer } from '@/components/agents/AgentComposer'
import { AgentConversation } from '@/components/agents/AgentConversation'
import { AgentInputQueue } from '@/components/agents/AgentInputQueue'
import { AgentInteractions } from '@/components/agents/AgentInteractions'
import { cancelAgentChatTurn } from '@/components/agents/cancel-agent-chat-turn'
import { MessageScrollerProvider } from '@/components/ui/message-scroller'

export function AgentPanelChat({
  selection,
  hidden,
  getContext,
  archived: panelArchived,
}: {
  selection: AgentPanelSelection
  hidden: boolean
  getContext: () => string
  archived: boolean
}) {
  const { org_id: orgID, project_id: projectID } = selection.scope
  const agentID = selection.agent_id
  const scope = { orgID, projectID, agentID }
  const chat = useAgentChat(scope, { source: 'web', getContext })
  const agentQuery = useAgentQuery(orgID, projectID, agentID, {
    refetchInterval: (detail) =>
      chat.isWorking ||
      detail?.mcp_connections.some((connection) => connection.state === 'initializing')
        ? 5_000
        : false,
  })
  const config = useAgentConfig(orgID, projectID, agentQuery.data?.agent.current_config_id)
  const interactions = useAgentInteractions(orgID, projectID, agentID)
  const cancel = useCancelAgent(orgID, projectID, agentID)
  const { data: me } = useMe()
  const currentActorId = useCurrentActorId(orgID, projectID, me.user.id)
  const archived = panelArchived || agentQuery.data?.agent.state === 'archived'
  const canOperate = selection.can_operate && !archived && agentQuery.isSuccess
  const canSendNow =
    canOperate &&
    interactions.data != null &&
    !interactions.data.data.some((interaction) => interaction.agent_id === agentID)
  const failedConnection = agentQuery.data?.mcp_connections.find(
    (connection) => connection.state === 'failed',
  )

  return (
    <div className={hidden ? 'hidden' : 'flex min-h-0 flex-1 flex-col'}>
      <MessageScrollerProvider autoScroll>
        {agentQuery.error != null && (
          <p role="alert" className="text-destructive p-3 text-sm">
            This agent could not be loaded. Check your access or choose another agent.
          </p>
        )}
        {failedConnection != null && (
          <p role="status" className="text-destructive p-3 text-sm">
            An MCP connection failed. Open the agent configuration to check its connection and
            credentials.
          </p>
        )}
        <div className="min-h-0 flex-1">
          <AgentConversation chat={chat} currentActorId={currentActorId} {...scope} />
        </div>
        <div className="max-h-[45%] shrink-0 space-y-2 overflow-y-auto p-3">
          {!archived && <AgentInteractions {...scope} canOperate={canOperate} />}
          {archived ? (
            <p className="text-muted-foreground text-sm">
              This agent is archived. Choose another agent to send messages.
            </p>
          ) : (
            <>
              <AgentInputQueue
                backlog={chat.inputBacklog}
                canOperate={canOperate}
                canSendNow={canSendNow}
              />
              <AgentComposer
                chat={chat}
                model={config.data?.model}
                cancelPending={cancel.isPending}
                cancelError={cancel.error}
                onCancel={() => cancelAgentChatTurn(chat, () => cancel.mutateAsync())}
                canOperate={canOperate}
                hidden={hidden}
                fileDropScope="composer"
              />
            </>
          )}
        </div>
      </MessageScrollerProvider>
    </div>
  )
}
