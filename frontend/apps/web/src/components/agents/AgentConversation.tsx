import type { UseAgentChatResult } from '@omnara/react'

import { AgentChatMessage } from '@/components/agents/AgentChatMessage'
import { Bot } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  MessageScroller,
  MessageScrollerButton,
  MessageScrollerContent,
  MessageScrollerItem,
  MessageScrollerProvider,
  MessageScrollerViewport,
} from '@/components/ui/message-scroller'
import { Skeleton } from '@/components/ui/skeleton'
import { useWebConfig } from '@/lib/web-config'

export function AgentConversation({
  chat,
  currentActorId,
  orgID,
  projectID,
}: {
  chat: UseAgentChatResult
  currentActorId?: string
  orgID: string
  projectID: string
}) {
  const { data: webConfig } = useWebConfig()
  const insufficientCredits = webConfig?.billingURL
    ? { billingHref: webConfig.billingHref }
    : undefined

  return (
    <MessageScrollerProvider>
      <MessageScroller>
        <MessageScrollerViewport>
          <MessageScrollerContent className="mx-auto w-full max-w-3xl px-4 pb-2 pt-6 sm:px-6">
            {chat.historyStatus === 'pending' ? (
              <div className="space-y-6 py-4">
                <Skeleton className="h-20 w-2/3" />
                <Skeleton className="ml-auto h-14 w-1/2" />
                <Skeleton className="h-28 w-3/4" />
              </div>
            ) : chat.historyStatus === 'error' ? (
              <div className="m-auto max-w-sm py-16 text-center">
                <p className="font-medium">Conversation unavailable</p>
                <p className="text-muted-foreground mt-1 text-sm">
                  Refresh the page to try loading it again.
                </p>
              </div>
            ) : chat.messages.length === 0 ? (
              <div className="m-auto flex max-w-sm flex-col items-center py-16 text-center">
                <div className="bg-muted mb-4 flex size-10 items-center justify-center rounded-full">
                  <Bot className="size-5" />
                </div>
                <p className="font-medium">Start a conversation</p>
                <p className="text-muted-foreground mt-1 text-sm">
                  Send a message to give this agent its next task.
                </p>
              </div>
            ) : (
              <>
                {chat.hasOlderMessages && (
                  <div className="flex justify-center pb-4">
                    <Button
                      variant="outline"
                      size="sm"
                      disabled={chat.isLoadingOlderMessages}
                      loading={chat.isLoadingOlderMessages}
                      onClick={chat.loadOlderMessages}
                    >
                      Load earlier messages
                    </Button>
                  </div>
                )}
                {chat.messages.map((message, index) => (
                  <MessageScrollerItem
                    key={message.id}
                    messageId={message.id}
                    scrollAnchor={index === chat.messages.length - 1}
                  >
                    <AgentChatMessage
                      message={message}
                      currentActorId={currentActorId}
                      orgID={orgID}
                      projectID={projectID}
                      insufficientCredits={insufficientCredits}
                    />
                  </MessageScrollerItem>
                ))}
              </>
            )}
          </MessageScrollerContent>
        </MessageScrollerViewport>
        <MessageScrollerButton />
      </MessageScroller>
    </MessageScrollerProvider>
  )
}
