import { useState, useEffect, useRef, useCallback } from 'react'
import { useParams } from 'react-router-dom'
import { InstanceHeader } from './InstanceHeader'
import { ChatInterface } from './../chat/ChatInterface'
import { apiClient } from '@/lib/dashboardApi'
import { InstanceDetail as IInstanceDetail, AgentStatus, Message } from '@/types/dashboard'

export function InstanceDetail() {
  const { instanceId } = useParams<{ instanceId: string }>()
  const [instance, setInstance] = useState<IInstanceDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  
  const [statusUpdating, setStatusUpdating] = useState(false)
  const [completingInstance, setCompletingInstance] = useState(false)
  const eventSourceRef = useRef<EventSource | null>(null)

  const fetchInstance = useCallback(async () => {
    if (!instanceId) return
    
    try {
      // Initially load only 50 most recent messages
      const instanceDetail = await apiClient.getInstanceDetail(instanceId, 50)
      setInstance(instanceDetail)
      setError(null)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch instance')
    } finally {
      setLoading(false)
    }
  }, [instanceId])

  const loadMoreMessages = async (beforeMessageId: string): Promise<Message[]> => {
    if (!instanceId) return []
    
    try {
      const messages = await apiClient.getInstanceMessages(instanceId, 50, beforeMessageId)
      return messages
    } catch (err) {
      console.error('Failed to load more messages:', err)
      return []
    }
  }

  // Set up SSE connection for real-time messages
  useEffect(() => {
    if (!instanceId) return

    // Close existing connection if any
    if (eventSourceRef.current) {
      eventSourceRef.current.close()
    }

    // Get SSE URL and create EventSource connection
    apiClient.getMessageStreamUrl(instanceId).then(streamUrl => {
      if (!streamUrl) return
      
      const eventSource = new EventSource(streamUrl)
      eventSourceRef.current = eventSource

      // Handle connection open
      eventSource.addEventListener('connected', (event) => {
        console.log('SSE connected:', event.data)
      })

      // Also handle onmessage for debugging
      eventSource.onmessage = (event) => {
        console.log('SSE onmessage received:', event)
      }

      // Handle new messages
      eventSource.addEventListener('message', (event) => {
        console.log('SSE message event received:', event.data)
        try {
          const messageData = JSON.parse(event.data)
          const newMessage: Message = {
            id: messageData.id,
            content: messageData.content,
            sender_type: messageData.sender_type,
            created_at: messageData.created_at,
            requires_user_input: messageData.requires_user_input
          }

          // Update instance with new message
          setInstance(prev => {
            if (!prev) return prev
            
            // Check if message already exists to avoid duplicates
            const messageExists = prev.messages.some(msg => msg.id === newMessage.id)
            if (messageExists) {
              return prev
            }
            
            return {
              ...prev,
              messages: [...prev.messages, newMessage],
              latest_message: newMessage.content,
              latest_message_at: newMessage.created_at,
              chat_length: prev.chat_length ? prev.chat_length + 1 : 1,
              status: newMessage.requires_user_input ? AgentStatus.AWAITING_INPUT : prev.status
            }
          })
        } catch (err) {
          console.error('Failed to parse message:', err)
        }
      })

      // Handle heartbeat
      eventSource.addEventListener('heartbeat', () => {
        // Just for keeping connection alive, no action needed
      })

      // Handle status updates
      eventSource.addEventListener('status_update', (event) => {
        console.log('SSE status_update event received:', event.data)
        try {
          const statusData = JSON.parse(event.data)
          
          // Update instance status
          setInstance(prev => {
            if (!prev) return prev
            console.log('Updating instance status to:', statusData.status)
            return {
              ...prev,
              status: statusData.status as AgentStatus
            }
          })
        } catch (err) {
          console.error('Failed to parse status update:', err)
        }
      })

      eventSource.addEventListener('message_update', (event) => {
        console.log('SSE message_update event received:', event.data)
        try {
          const messageData = JSON.parse(event.data)
          
          // Update the specific message in the messages array
          setInstance(prev => {
            if (!prev) return prev
            console.log('Updating message:', messageData.id, 'requires_user_input:', messageData.requires_user_input)
            return {
              ...prev,
              messages: prev.messages.map(msg => 
                msg.id === messageData.id 
                  ? { ...msg, requires_user_input: messageData.requires_user_input }
                  : msg
              )
            }
          })
        } catch (err) {
          console.error('Failed to parse message update:', err)
        }
      })

      eventSource.addEventListener('git_diff_update', (event) => {
        console.log('SSE git_diff_update event received:', event.data)
        try {
          const gitDiffData = JSON.parse(event.data)
          
          // Update instance git_diff
          setInstance(prev => {
            if (!prev) return prev
            console.log('Updating git_diff for instance:', gitDiffData.instance_id)
            return {
              ...prev,
              git_diff: gitDiffData.git_diff
            }
          })
        } catch (err) {
          console.error('Failed to parse git_diff update:', err)
        }
      })

      // Agent heartbeat updates (presence)
      eventSource.addEventListener('agent_heartbeat', (event) => {
        console.log('SSE agent_heartbeat event received:', event.data)
        try {
          const presenceData = JSON.parse(event.data)
          if (!presenceData.last_heartbeat_at) return
          setInstance(prev => {
            if (!prev) return prev
            return {
              ...prev,
              last_heartbeat_at: presenceData.last_heartbeat_at,
            }
          })
        } catch (err) {
          console.error('Failed to parse agent heartbeat:', err)
        }
      })

      // Handle errors
      eventSource.addEventListener('error', (event) => {
        console.error('SSE error event:', event)
        if (eventSource.readyState === EventSource.CLOSED) {
          console.log('SSE connection closed, attempting to reconnect...')
          // Browser will automatically reconnect
        }
      })

      // Also add onerror handler
      eventSource.onerror = (error) => {
        console.error('SSE onerror:', error, 'readyState:', eventSource.readyState)
      }
    })

    // Cleanup on unmount
    return () => {
      if (eventSourceRef.current) {
        eventSourceRef.current.close()
        eventSourceRef.current = null
      }
    }
  }, [instanceId])

  useEffect(() => {
    fetchInstance()
  }, [fetchInstance])

  const handleMessageSubmit = async (content: string) => {
    if (!instanceId) return
    
    try {
      await apiClient.submitUserMessage(instanceId, content)
      // No need to refresh - SSE will push the new message
    } catch (err) {
      console.error('Failed to submit message:', err)
      throw err // Re-throw for ChatInterface to handle
    }
  }

  const handleStatusUpdate = async (newStatus: AgentStatus) => {
    if (!instanceId || statusUpdating) return
    
    setStatusUpdating(true)
    try {
      await apiClient.updateInstanceStatus(instanceId, newStatus)
      fetchInstance() // Refresh data
    } catch (err) {
      console.error('Failed to update status:', err)
    } finally {
      setStatusUpdating(false)
    }
  }

  const handlePauseResume = async () => {
    if (!instanceId || statusUpdating) return
    
    setStatusUpdating(true)
    try {
      if (instance?.status === AgentStatus.PAUSED) {
        await apiClient.resumeAgent(instanceId)
      } else {
        await apiClient.pauseAgent(instanceId)
      }
      fetchInstance() // Refresh data
    } catch (err) {
      console.error('Failed to pause/resume agent:', err)
    } finally {
      setStatusUpdating(false)
    }
  }

  const handleKill = async () => {
    if (!instanceId || statusUpdating) return
    
    setStatusUpdating(true)
    try {
      await apiClient.killAgent(instanceId)
      fetchInstance() // Refresh data
    } catch (err) {
      console.error('Failed to kill agent:', err)
    } finally {
      setStatusUpdating(false)
    }
  }

  const handleMarkCompleted = async () => {
    if (!instanceId || completingInstance) return
    
    setCompletingInstance(true)
    try {
      await apiClient.updateInstanceStatus(instanceId, AgentStatus.COMPLETED)
      fetchInstance() // Refresh data
    } catch (err) {
      console.error('Failed to mark as completed:', err)
    } finally {
      setCompletingInstance(false)
    }
  }

  // Esc to interrupt feature temporarily disabled
  /*
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      const status = instance?.status
      const canInterrupt =
        status &&
        status !== AgentStatus.COMPLETED &&
        status !== AgentStatus.KILLED &&
        status !== AgentStatus.PAUSED &&
        status !== AgentStatus.AWAITING_INPUT
      if (!canInterrupt || statusUpdating) return

      // Ignore Esc when typing in inputs/textarea/select or contentEditable
      const target = e.target as HTMLElement | null
      const tag = target?.tagName?.toLowerCase()
      const isTyping =
        tag === 'input' ||
        tag === 'textarea' ||
        tag === 'select' ||
        target?.isContentEditable
      if (isTyping) return

      e.preventDefault()
      const proceed = window.confirm('Interrupt the agent? It will pause and wait for your instructions.')
      if (proceed && instanceId) {
        // Send a control message that the wrapper listens for
        apiClient
          .submitUserMessage(instanceId, '__OMNARA_ESC_INTERRUPT__')
          .catch((err) => {
            console.error('Failed to interrupt agent:', err)
          })
      }
    }

    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [instanceId, instance?.status, statusUpdating])
  */

  if (loading) {
    return (
      <div className="flex items-center justify-center min-h-[400px]">
        <div className="text-off-white/80 animate-fade-in">Loading instance details...</div>
      </div>
    )
  }

  if (error || !instance) {
    return (
      <div className="flex items-center justify-center min-h-[400px]">
        <div className="text-red-400 animate-fade-in">Error: {error || 'Instance not found'}</div>
      </div>
    )
  }

  const isWaitingForInput = instance.status === AgentStatus.AWAITING_INPUT

  return (
    <div className="animate-fade-in h-full flex flex-col">
      <div className="px-6 py-6 border-b border-border-divider flex-shrink-0">
        <InstanceHeader 
          instance={instance} 
          isWaitingForInput={isWaitingForInput}
          onMarkCompleted={handleMarkCompleted}
          completingInstance={completingInstance}
        />
      </div>

      <div className="relative flex-1 min-h-0 px-6 pb-6">
        <ChatInterface
          key={instance.id} // Force React to create a new component instance for each chat
          instance={instance}
          onMessageSubmit={handleMessageSubmit}
          onLoadMoreMessages={loadMoreMessages}
        />
      </div>
    </div>
  )
}
