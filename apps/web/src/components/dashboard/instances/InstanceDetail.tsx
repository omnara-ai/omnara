import { useState, useEffect, useRef, useCallback } from 'react'
import { useParams } from 'react-router-dom'
import { InstanceHeader } from './InstanceHeader'
import { ChatInterface } from './../chat/ChatInterface'
import { apiClient } from '@/lib/dashboardApi'
import {
  InstanceDetail as IInstanceDetail,
  AgentStatus,
  Message,
  InstanceShare,
  InstanceAccessLevel,
} from '@/types/dashboard'
import { Share } from 'lucide-react'
import { reportError, reportMessage } from '@/integrations/sentry'

const SENTRY_TAGS = { feature: 'instance-detail' }

export function InstanceDetail() {
  const { instanceId } = useParams<{ instanceId: string }>()
  const [instance, setInstance] = useState<IInstanceDetail | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  
  const [statusUpdating, setStatusUpdating] = useState(false)
  const [completingInstance, setCompletingInstance] = useState(false)
  const eventSourceRef = useRef<EventSource | null>(null)
  const [shareMenuOpen, setShareMenuOpen] = useState(false)
  const [shares, setShares] = useState<InstanceShare[]>([])
  const [sharesLoading, setSharesLoading] = useState(false)
  const [sharesLoaded, setSharesLoaded] = useState(false)
  const [shareEmail, setShareEmail] = useState('')
  const [shareAccess, setShareAccess] = useState<InstanceAccessLevel>(InstanceAccessLevel.WRITE)
  const [shareSubmitting, setShareSubmitting] = useState(false)
  const [shareError, setShareError] = useState<string | null>(null)
  const shareMenuRef = useRef<HTMLDivElement | null>(null)

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
      reportError(err, {
        context: 'Failed to load additional messages',
        extras: { instanceId, beforeMessageId },
        tags: SENTRY_TAGS,
      })
      return []
    }
  }

  const sortShares = useCallback((entries: InstanceShare[]) => {
    const owner = entries.find(entry => entry.is_owner)
    const others = entries.filter(entry => !entry.is_owner)
    return owner ? [owner, ...others] : others
  }, [])

  const loadInstanceShares = useCallback(async () => {
    if (!instanceId) return
    setSharesLoading(true)
    try {
      const data = await apiClient.getInstanceAccessList(instanceId)
      setShares(sortShares(data))
      setShareError(null)
      setSharesLoaded(true)
    } catch (err) {
      console.error('Failed to load shared users:', err)
      setShareError(err instanceof Error ? err.message : 'Failed to load access list')
    } finally {
      setSharesLoading(false)
    }
  }, [instanceId, sortShares])

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
        // SSE connection established
      })

      // Also handle onmessage for debugging
      eventSource.onmessage = (event) => {
        // Default message handler
      }

      // Handle new messages
      eventSource.addEventListener('message', (event) => {
        try {
          const messageData = JSON.parse(event.data)
          const newMessage: Message = {
            id: messageData.id,
            content: messageData.content,
            sender_type: messageData.sender_type,
            created_at: messageData.created_at,
            requires_user_input: messageData.requires_user_input,
            sender_user_id: messageData.sender_user_id ?? null,
            sender_user_email: messageData.sender_user_email ?? null,
            sender_user_display_name: messageData.sender_user_display_name ?? null,
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
          reportError(err, {
            context: 'Failed to parse SSE message payload',
            extras: { instanceId, raw: event.data },
            tags: SENTRY_TAGS,
          })
        }
      })

      // Handle heartbeat
      eventSource.addEventListener('heartbeat', () => {
        // Just for keeping connection alive, no action needed
      })

      // Handle status updates
      eventSource.addEventListener('status_update', (event) => {
        try {
          const statusData = JSON.parse(event.data)
          
          // Update instance status
          setInstance(prev => {
            if (!prev) return prev
            return {
              ...prev,
              status: statusData.status as AgentStatus
            }
          })
        } catch (err) {
          reportError(err, {
            context: 'Failed to parse SSE status update',
            extras: { instanceId, raw: event.data },
            tags: SENTRY_TAGS,
          })
        }
      })

      eventSource.addEventListener('message_update', (event) => {
        try {
          const messageData = JSON.parse(event.data)
          
          // Update the specific message in the messages array
          setInstance(prev => {
            if (!prev) return prev

            let matched = false
            const messages = prev.messages.map(msg => {
              if (msg.id === messageData.id) {
                matched = true
                return { ...msg, requires_user_input: messageData.requires_user_input }
              }
              return msg
            })

            if (!matched) {
              reportMessage('Received message_update for unknown message', {
                context: 'Missing base message for SSE update',
                extras: { instanceId, messageId: messageData.id },
                tags: SENTRY_TAGS,
              })
              return prev
            }

            // Keep the instance status in sync with the message-level change 
            // so the UI updates immediately.
            let nextStatus = prev.status
            if (messageData.requires_user_input) {
              if (
                prev.status !== AgentStatus.COMPLETED &&
                prev.status !== AgentStatus.FAILED &&
                prev.status !== AgentStatus.KILLED
              ) {
                nextStatus = AgentStatus.AWAITING_INPUT
              }
            } else if (prev.status === AgentStatus.AWAITING_INPUT) {
              nextStatus = AgentStatus.ACTIVE
            }

            return {
              ...prev,
              messages,
              status: nextStatus,
            }
          })
        } catch (err) {
          reportError(err, {
            context: 'Failed to parse SSE message update',
            extras: { instanceId, raw: event.data },
            tags: SENTRY_TAGS,
          })
        }
      })

      eventSource.addEventListener('git_diff_update', (event) => {
        try {
          const gitDiffData = JSON.parse(event.data)
          
          // Update instance git_diff
          setInstance(prev => {
            if (!prev) return prev
            return {
              ...prev,
              git_diff: gitDiffData.git_diff
            }
          })
        } catch (err) {
          reportError(err, {
            context: 'Failed to parse SSE git diff update',
            extras: { instanceId, raw: event.data?.slice?.(0, 1000) },
            tags: SENTRY_TAGS,
          })
        }
      })

      // Agent heartbeat updates (presence)
      eventSource.addEventListener('agent_heartbeat', (event) => {
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
          reportError(err, {
            context: 'Failed to parse SSE agent heartbeat',
            extras: { instanceId, raw: event.data },
            tags: SENTRY_TAGS,
          })
        }
      })

      // Handle errors
      eventSource.addEventListener('error', (event) => {
        reportError(new Error('SSE error event'), {
          context: 'EventSource error received',
          extras: { instanceId, state: eventSource.readyState },
          tags: SENTRY_TAGS,
        })
        if (eventSource.readyState === EventSource.CLOSED) {
          // SSE connection closed, attempting to reconnect...
          // Browser will automatically reconnect
        }
      })

      // Also add onerror handler
      eventSource.onerror = (error) => {
        reportError(error, {
          context: 'EventSource onerror fired',
          extras: { instanceId, state: eventSource.readyState },
          tags: SENTRY_TAGS,
        })
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

  useEffect(() => {
    setShares([])
    setSharesLoaded(false)
    setShareMenuOpen(false)
    setShareEmail('')
    setShareAccess(InstanceAccessLevel.WRITE)
    setShareError(null)
  }, [instanceId])

  useEffect(() => {
    if (instance && instance.is_owner && !sharesLoaded) {
      loadInstanceShares()
    }
  }, [instance, sharesLoaded, loadInstanceShares])

  useEffect(() => {
    if (!shareMenuOpen) return

    const handleClickOutside = (event: MouseEvent) => {
      if (
        shareMenuRef.current &&
        event.target instanceof Node &&
        !shareMenuRef.current.contains(event.target)
      ) {
        setShareMenuOpen(false)
      }
    }

    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [shareMenuOpen])

  const handleToggleShareMenu = () => {
    if (!instance || !instance.is_owner) return
    if (!shareMenuOpen && !sharesLoaded) {
      loadInstanceShares()
    }
    setShareMenuOpen(prev => !prev)
  }

  const handleAddShare = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!instanceId || !shareEmail.trim() || shareSubmitting) return
    setShareSubmitting(true)
    try {
      const newShare = await apiClient.addInstanceShare(instanceId, {
        email: shareEmail.trim(),
        access: shareAccess,
      })
      setShares(prev => sortShares([...prev.filter(s => s.id !== newShare.id), newShare]))
      setShareEmail('')
      setShareAccess(InstanceAccessLevel.WRITE)
      setShareError(null)
    } catch (err) {
      console.error('Failed to add share:', err)
      setShareError(err instanceof Error ? err.message : 'Failed to add user')
    } finally {
      setShareSubmitting(false)
    }
  }

  const handleRemoveShare = async (shareId: string) => {
    if (!instanceId) return
    try {
      await apiClient.removeInstanceShare(instanceId, shareId)
      setShares(prev => prev.filter(entry => entry.id !== shareId))
      setShareError(null)
    } catch (err) {
      console.error('Failed to remove share:', err)
      setShareError(err instanceof Error ? err.message : 'Failed to remove user')
    }
  }

  const handleMessageSubmit = async (content: string) => {
    if (!instanceId) return
    
    try {
      await apiClient.submitUserMessage(instanceId, content)
      // No need to refresh - SSE will push the new message
    } catch (err) {
      reportError(err, {
        context: 'Failed to submit user message',
        extras: { instanceId },
        tags: SENTRY_TAGS,
      })
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
      reportError(err, {
        context: 'Failed to update instance status',
        extras: { instanceId, newStatus },
        tags: SENTRY_TAGS,
      })
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
      reportError(err, {
        context: 'Failed to pause or resume agent',
        extras: { instanceId, currentStatus: instance?.status },
        tags: SENTRY_TAGS,
      })
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
      reportError(err, {
        context: 'Failed to kill agent',
        extras: { instanceId },
        tags: SENTRY_TAGS,
      })
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
      reportError(err, {
        context: 'Failed to mark agent as completed',
        extras: { instanceId },
        tags: SENTRY_TAGS,
      })
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
  const canManageSharing = Boolean(instance.is_owner)

  return (
    <div className="animate-fade-in h-full flex flex-col">
      <div className="px-6 py-6 border-b border-border-divider flex-shrink-0">
        <div className="flex items-start justify-between gap-4">
          <div className="flex-1">
          <InstanceHeader 
            instance={instance} 
            isWaitingForInput={isWaitingForInput}
            onMarkCompleted={instance.is_owner ? handleMarkCompleted : undefined}
            completingInstance={instance.is_owner ? completingInstance : false}
          />
          </div>
          {instance.is_owner && (
            <div className="relative" ref={shareMenuRef}>
              <button
                onClick={handleToggleShareMenu}
                className="p-2 rounded-full bg-surface-panel text-text-primary border border-border-divider hover:bg-interactive-hover transition-colors"
                aria-label="Manage sharing"
              >
                <Share className="w-5 h-5" />
              </button>
              {shareMenuOpen && (
                <div className="absolute right-0 mt-2 w-80 rounded-xl border border-border-divider bg-surface-panel/95 backdrop-blur shadow-xl p-4 space-y-4 z-20">
                  <div>
                    <h3 className="text-sm font-semibold text-text-primary">Shared Access</h3>
                    <p className="text-xs text-text-secondary mt-1">
                      Manage who can view or edit this session.
                    </p>
                  </div>
                  {sharesLoading ? (
                    <div className="text-sm text-text-secondary">Loading...</div>
                  ) : (
                    <div className="space-y-2 max-h-60 overflow-y-auto pr-1">
                      {shares.length === 0 ? (
                        <div className="text-sm text-text-secondary">No one else has access yet.</div>
                      ) : (
                        shares.map(share => (
                          <div
                            key={share.id}
                            className="flex items-center justify-between rounded-lg border border-border-divider/60 bg-white/5 px-3 py-2"
                          >
                            <div>
                              <div className="text-sm font-medium text-text-primary">
                                {share.email}
                                {share.is_owner && ' (Owner)'}
                              </div>
                              <div className="text-xs text-text-secondary">
                                {share.display_name ? `${share.display_name} • ` : ''}
                                {share.access === InstanceAccessLevel.WRITE ? 'Write access' : 'Read access'}
                                {share.invited ? ' • Pending' : ''}
                              </div>
                            </div>
                            {!share.is_owner && (
                              <button
                                onClick={() => handleRemoveShare(share.id)}
                                className="text-xs text-red-400 hover:text-red-300"
                              >
                                Remove
                              </button>
                            )}
                          </div>
                        ))
                      )}
                    </div>
                  )}
                  <form onSubmit={handleAddShare} className="space-y-2">
                    <div>
                      <label className="block text-xs font-semibold text-text-secondary uppercase tracking-wide">
                        Invite by email
                      </label>
                      <input
                        type="email"
                        value={shareEmail}
                        onChange={e => setShareEmail(e.target.value)}
                        className="mt-1 w-full rounded-md border border-border-divider bg-transparent px-3 py-2 text-sm text-text-primary focus:border-electric-accent focus:outline-none"
                        placeholder="user@example.com"
                        required
                      />
                    </div>
                    <div className="flex items-center justify-between">
                      <label className="text-xs font-semibold text-text-secondary uppercase tracking-wide">
                        Access level
                      </label>
                      <select
                        value={shareAccess}
                        onChange={e => setShareAccess(e.target.value as InstanceAccessLevel)}
                        className="rounded-md border border-border-divider bg-transparent px-2 py-1 text-sm text-text-primary focus:border-electric-accent focus:outline-none"
                      >
                        <option value={InstanceAccessLevel.WRITE}>Write</option>
                        <option value={InstanceAccessLevel.READ}>Read</option>
                      </select>
                    </div>
                    {shareError && (
                      <div className="text-xs text-red-400">{shareError}</div>
                    )}
                    <button
                      type="submit"
                      disabled={shareSubmitting}
                      className="w-full rounded-md bg-interactive-primary py-2 text-sm font-medium text-white hover:bg-interactive-primary/90 disabled:opacity-50 disabled:cursor-not-allowed"
                    >
                      {shareSubmitting ? 'Adding...' : 'Add' }
                    </button>
                  </form>
                </div>
              )}
            </div>
          )}
        </div>
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
