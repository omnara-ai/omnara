import { useState, useEffect, useRef } from 'react'
import { ChatMessage } from './ChatMessage'
import { ChatInput } from './ChatInput'
import { GitDiffView } from '../git-diff/GitDiffView'
import { Message, InstanceDetail as IInstanceDetail, InstanceAccessLevel } from '@/types/dashboard'
import { formatAgentTypeName } from '@/utils/statusUtils'
import { ChatWorkingIndicator } from './ChatWorkingIndicator'
import { useAuth } from '@/lib/auth/AuthContext'

export interface MessageGroup {
  id: string
  agentName: string
  timestamp: string
  messages: Message[]
  isFromAgent: boolean
}

interface ChatInterfaceProps {
  instance: IInstanceDetail
  onMessageSubmit: (content: string) => Promise<void>
  onLoadMoreMessages: (beforeMessageId: string) => Promise<Message[]>
}

// Messages are already sorted by the backend

function groupMessages(messages: Message[], agentName: string, currentUserId?: string | null): MessageGroup[] {
  const groups: MessageGroup[] = []
  let currentGroup: MessageGroup | null = null

  messages.forEach(message => {
    const isFromAgent = message.sender_type === 'AGENT'
    const isCurrentUser = !isFromAgent &&
      !!currentUserId &&
      !!message.sender_user_id &&
      message.sender_user_id === currentUserId

    const sender = isFromAgent
      ? agentName
      : isCurrentUser
        ? 'You'
        : message.sender_user_display_name?.trim() || message.sender_user_email || 'Member'
    
    // Check if we should start a new group
    const shouldStartNewGroup = !currentGroup || 
      currentGroup.isFromAgent !== isFromAgent ||
      currentGroup.agentName !== sender ||
      // Start new group if messages are more than 5 minutes apart
      new Date(message.created_at).getTime() - new Date(currentGroup.timestamp).getTime() > 5 * 60 * 1000
    
    if (shouldStartNewGroup) {
      currentGroup = {
        id: `group-${message.id}`,
        agentName: sender,
        timestamp: message.created_at,
        messages: [message],
        isFromAgent
      }
      groups.push(currentGroup)
    } else {
      currentGroup!.messages.push(message)
    }
  })
  
  return groups
}

export function ChatInterface({ instance, onMessageSubmit, onLoadMoreMessages }: ChatInterfaceProps) {
  const { user } = useAuth()
  const [allMessages, setAllMessages] = useState<Message[]>([])
  const [messageGroups, setMessageGroups] = useState<MessageGroup[]>([])
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [isLoadingMore, setIsLoadingMore] = useState(false)
  const [hasMoreMessages, setHasMoreMessages] = useState(true)
  const chatContainerRef = useRef<HTMLDivElement>(null)
  const isAtBottomRef = useRef(true)
  const loadingRef = useRef(false)
  const normalizedAccessLevel =
    typeof instance.access_level === 'string'
      ? instance.access_level.toUpperCase()
      : instance.access_level
  const canWrite = normalizedAccessLevel === InstanceAccessLevel.WRITE

  // Check if user is at bottom of scroll
  const checkIfAtBottom = () => {
    if (!chatContainerRef.current) return false
    const { scrollTop, scrollHeight, clientHeight } = chatContainerRef.current
    // Consider "at bottom" if within 50px of the bottom
    return scrollHeight - scrollTop - clientHeight < 50
  }

  // Handle scroll events to track if user is at bottom and trigger pagination
  const handleScroll = async () => {
    if (!chatContainerRef.current) return
    
    const { scrollTop } = chatContainerRef.current
    isAtBottomRef.current = checkIfAtBottom()
    
    // Check if we should load more messages (scrolled to the very top)
    // Only trigger if not already loading and we have more messages
    if (scrollTop === 0 && !isLoadingMore && hasMoreMessages && allMessages.length > 0) {
      // Prevent concurrent requests
      if (loadingRef.current) return
      loadingRef.current = true
      
      setIsLoadingMore(true)
      
      // Get the oldest message ID for cursor-based pagination
      const oldestMessage = allMessages[0]
      if (oldestMessage) {
        // Save current scroll position
        const scrollContainer = chatContainerRef.current
        const previousScrollHeight = scrollContainer.scrollHeight
        const previousScrollTop = scrollContainer.scrollTop
        
        try {
          // Load more messages
          const newMessages = await onLoadMoreMessages(oldestMessage.id)
          
          if (newMessages.length > 0) {
            // Merge new messages (avoiding duplicates)
            setAllMessages(prev => {
              const messageIds = new Set(prev.map(m => m.id))
              const uniqueNewMessages = newMessages
                .filter(m => !messageIds.has(m.id))
                .map(m => ({
                  id: m.id,
                  content: m.content,
                  sender_type: m.sender_type,
                  created_at: m.created_at,
                  requires_user_input: m.requires_user_input,
                  sender_user_id: m.sender_user_id,
                  sender_user_email: m.sender_user_email,
                  sender_user_display_name: m.sender_user_display_name,
                  message_type: m.message_type,
                  message_metadata: m.message_metadata,
                } as Message))
              
              // Prepend new messages and sort by created_at
              const combined = [...uniqueNewMessages, ...prev]
              combined.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
              return combined
            })
            
            // Wait for DOM to update then restore scroll position
            await new Promise(resolve => {
              setTimeout(() => {
                if (scrollContainer) {
                  // Calculate how much height was added by new messages
                  const newScrollHeight = scrollContainer.scrollHeight
                  const heightDifference = newScrollHeight - previousScrollHeight
                  
                  // Adjust scroll position to keep viewing the same messages
                  scrollContainer.scrollTop = previousScrollTop + heightDifference
                }
                resolve(undefined)
              }, 100) // Allow time for React to render the new messages
            })
            
            // Brief cooldown to prevent accidental rapid loading
            await new Promise(resolve => setTimeout(resolve, 300))
          } else {
            // No more messages to load
            setHasMoreMessages(false)
          }
        } finally {
          setIsLoadingMore(false)
          loadingRef.current = false
        }
      }
    }
  }

  // Merge instance messages (from SSE or initial load) with existing paginated messages
  useEffect(() => {
    setAllMessages(prev => {
      const messageMap = new Map<string, Message>()
      
      // Add existing paginated messages first
      prev.forEach(msg => messageMap.set(msg.id, msg))
      
      // Add/update with instance messages (from SSE or initial load)
      instance.messages.forEach(msg => {
        messageMap.set(msg.id, {
          id: msg.id,
          content: msg.content,
          sender_type: msg.sender_type,
          created_at: msg.created_at,
          requires_user_input: msg.requires_user_input,
          sender_user_id: msg.sender_user_id,
          sender_user_email: msg.sender_user_email,
          sender_user_display_name: msg.sender_user_display_name,
          message_type: msg.message_type,
          message_metadata: msg.message_metadata,
        })
      })
      
      // Convert to array and sort chronologically
      const combined = Array.from(messageMap.values())
      combined.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime())
      return combined
    })
    
    // Determine if there are more messages to load based on initial load
    // If we get exactly our page size (50), there might be more
    if (instance.messages.length === 50) {
      setHasMoreMessages(true)
    } else if (instance.messages.length < 50) {
      setHasMoreMessages(false)
    }
  }, [instance.messages])
  
  // Group messages whenever allMessages changes
  useEffect(() => {
    const agentDisplayName = formatAgentTypeName(instance.agent_type_name || instance.agent_type || 'Agent')
    const groups = groupMessages(allMessages, agentDisplayName, user?.id)
    setMessageGroups(groups)
  }, [allMessages, instance.agent_type_name, instance.agent_type, user?.id])

  // Smart scroll: only scroll to bottom if user was already at bottom
  useEffect(() => {
    if (messageGroups.length > 0 && chatContainerRef.current && isAtBottomRef.current) {
      chatContainerRef.current.scrollTop = chatContainerRef.current.scrollHeight
    }
  }, [messageGroups])

  const handleSubmitMessage = async (content: string) => {
    if (!canWrite) {
      return
    }
    setIsSubmitting(true)
    try {
      await onMessageSubmit(content)
    } finally {
      setIsSubmitting(false)
    }
  }

  // Check if we're waiting for user input
  const isWaitingForInput = instance.status === 'AWAITING_INPUT'
  
  // Get the current waiting message from allMessages
  const lastMessage = allMessages.length > 0 ? allMessages[allMessages.length - 1] : null
  const currentWaitingMessage = isWaitingForInput && lastMessage?.requires_user_input && lastMessage.sender_type === 'AGENT'
    ? lastMessage
    : null

  // Determine if we should show the working/offline indicator in chat
  const ttlSeconds = 60
  const lastHeartbeat = instance.last_heartbeat_at ? new Date(instance.last_heartbeat_at).getTime() : null
  const secondsSince = lastHeartbeat ? Math.floor((Date.now() - lastHeartbeat) / 1000) : null
  const isOnline = secondsSince !== null && secondsSince <= ttlSeconds
  const shouldShowActivityIndicator = 
    instance.status !== 'COMPLETED' &&
    !isWaitingForInput &&
    (!isOnline || ['ACTIVE', 'PAUSED', 'FAILED', 'KILLED'].includes(instance.status))

  return (
    <div className="absolute inset-0 flex flex-col">
      {/* Chat Messages Area - Takes up remaining space */}
      <div ref={chatContainerRef} onScroll={handleScroll} className="flex-1 overflow-y-auto px-12 pt-6">
        {/* Loading indicator at top */}
        {isLoadingMore && (
          <div className="flex justify-center py-2 mb-4">
            <div className="text-text-secondary text-sm">Loading more messages...</div>
          </div>
        )}
        {messageGroups.length === 0 ? (
          <div className="flex items-center justify-center h-full">
            <div className="text-center space-y-2">
              <div className="text-text-primary text-lg">No activity yet</div>
              <div className="text-sm text-text-secondary">
                Messages will appear here as the agent works
              </div>
            </div>
          </div>
        ) : (
          <div className="space-y-8 pb-6">
            {messageGroups.map((group, groupIndex) => {
              // Check if this is the last group
              const isLastGroup = groupIndex === messageGroups.length - 1
              // Check if the last message in the last group requires input
              const lastMessage = isLastGroup ? group.messages[group.messages.length - 1] : null
              const showWaitingIndicator = isLastGroup && 
                lastMessage?.requires_user_input && 
                lastMessage?.sender_type === 'AGENT' &&
                instance.status === 'AWAITING_INPUT'
                
              return (
                <div key={group.id} className="mb-6">
                  <ChatMessage 
                    messageGroup={group} 
                    showWaitingIndicator={showWaitingIndicator}
                  />
                </div>
              )
            })}
          </div>
        )}
      </div>

      {/* Bottom section - Git Diff and Input */}
      <div className="mt-6 px-12 pb-12">
        {/* Git Diff View - above input */}
        {instance.git_diff && (
          <GitDiffView 
            gitDiff={instance.git_diff} 
            hasInputBelow={instance.status !== 'COMPLETED'}
            className={instance.status !== 'COMPLETED' ? 'mb-3' : ''}
          />
        )}

        {/* Activity/Status Indicator - subtle inline banner above input */}
        {shouldShowActivityIndicator && (
          <div className="mb-3">
            <ChatWorkingIndicator instance={instance} />
          </div>
        )}

        {/* Input Area - Fixed at bottom */}
        {instance.status !== 'COMPLETED' && (
          <ChatInput
            isWaitingForInput={isWaitingForInput}
            currentWaitingMessage={currentWaitingMessage}
            onMessageSubmit={handleSubmitMessage}
            isSubmitting={isSubmitting}
            hasGitDiff={!!instance.git_diff}
            canWrite={canWrite}
          />
        )}
      </div>
    </div>
  )
}
