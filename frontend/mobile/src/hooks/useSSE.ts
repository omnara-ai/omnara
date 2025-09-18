import { useEffect, useRef } from 'react';
import EventSource from 'react-native-sse';
import { dashboardApi } from '@/services/api';
import { Message, AgentStatus } from '@/types';
import { reportError, reportMessage } from '@/lib/sentry';

interface UseSSEProps {
  instanceId: string;
  enabled: boolean;
  onMessage: (message: Message) => void;
  onStatusUpdate: (status: AgentStatus) => void;
  onMessageUpdate: (messageId: string, requiresUserInput: boolean) => void;
  onGitDiffUpdate?: (gitDiff: string | null) => void;
  onError?: (error: Error) => void;
}

export function useSSE({
  instanceId,
  enabled,
  onMessage,
  onStatusUpdate,
  onMessageUpdate,
  onGitDiffUpdate,
  onError,
}: UseSSEProps) {
  const eventSourceRef = useRef<EventSource | null>(null);
  const isConnectingRef = useRef(false);
  const sentryTags = { feature: 'mobile-sse', instanceId };

  useEffect(() => {
    if (!enabled || !instanceId) {
      // Clean up if disabled
      if (eventSourceRef.current) {
        console.log('[useSSE] Disconnecting (disabled)');
        eventSourceRef.current.close();
        eventSourceRef.current = null;
        isConnectingRef.current = false;
      }
      return;
    }

    // Prevent multiple simultaneous connections
    if (isConnectingRef.current || eventSourceRef.current) {
      console.log('[useSSE] Connection already exists or in progress, skipping');
      return;
    }

    const connect = async () => {
      try {
        isConnectingRef.current = true;
        const streamUrl = await dashboardApi.getMessageStreamUrl(instanceId);
        if (!streamUrl) {
          reportMessage('[useSSE] No stream URL available', {
            context: 'Missing SSE stream URL',
            extras: { instanceId },
            tags: sentryTags,
          });
          return;
        }

        console.log('[useSSE] Connecting to SSE for instance:', instanceId);
        const eventSource = new EventSource(streamUrl);
        eventSourceRef.current = eventSource;

        eventSource.addEventListener('open', () => {
          console.log('[useSSE] Connected successfully');
          isConnectingRef.current = false;
        });

        eventSource.addEventListener('message', (event: any) => {
          try {
            const data = JSON.parse(event.data);
            const message: Message = {
              id: data.id,
              content: data.content,
              sender_type: data.sender_type,
              created_at: data.created_at,
              requires_user_input: data.requires_user_input,
              sender_user_id: data.sender_user_id ?? null,
              sender_user_email: data.sender_user_email ?? null,
              sender_user_display_name: data.sender_user_display_name ?? null,
            };
            onMessage(message);
          } catch (err) {
            reportError(err, {
              context: 'Failed to parse SSE message payload',
              extras: { raw: event.data, instanceId },
              tags: sentryTags,
            });
          }
        });

        eventSource.addEventListener('status_update', (event: any) => {
          try {
            const data = JSON.parse(event.data);
            onStatusUpdate(data.status);
          } catch (err) {
            reportError(err, {
              context: 'Failed to parse SSE status update',
              extras: { raw: event.data, instanceId },
              tags: sentryTags,
            });
          }
        });

        eventSource.addEventListener('message_update', (event: any) => {
          try {
            const data = JSON.parse(event.data);
            onMessageUpdate(data.id, data.requires_user_input);
          } catch (err) {
            reportError(err, {
              context: 'Failed to parse SSE message update',
              extras: { raw: event.data, instanceId },
              tags: sentryTags,
            });
          }
        });

        eventSource.addEventListener('git_diff_update', (event: any) => {
          try {
            const data = JSON.parse(event.data);
            console.log('[useSSE] Git diff update received for instance:', data.instance_id);
            if (onGitDiffUpdate) {
              onGitDiffUpdate(data.git_diff);
            }
          } catch (err) {
            reportError(err, {
              context: 'Failed to parse SSE git diff update',
              extras: { raw: event.data, instanceId },
              tags: sentryTags,
            });
          }
        });

        eventSource.addEventListener('error', (error: any) => {
          reportError(error, {
            context: 'SSE connection error event',
            extras: { instanceId },
            tags: sentryTags,
          });
          isConnectingRef.current = false;
          if (onError) {
            onError(new Error('SSE connection error'));
          }
        });
      } catch (error) {
        reportError(error, {
          context: 'Failed to establish SSE connection',
          extras: { instanceId },
          tags: sentryTags,
        });
        isConnectingRef.current = false;
        if (onError) {
          onError(error as Error);
        }
      }
    };

    connect();

    return () => {
      if (eventSourceRef.current) {
        console.log('[useSSE] Disconnecting (cleanup)');
        eventSourceRef.current.close();
        eventSourceRef.current = null;
        isConnectingRef.current = false;
      }
    };
  }, [instanceId, enabled]); // Only reconnect if these change

  return null;
}
