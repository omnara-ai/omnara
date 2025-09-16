import { useEffect, useRef } from 'react';
import EventSource from 'react-native-sse';
import { dashboardApi } from '@/services/api';
import { Message, AgentStatus } from '@/types';

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
          console.error('[useSSE] No stream URL available');
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
            };
            onMessage(message);
          } catch (err) {
            console.error('[useSSE] Failed to parse message:', err);
          }
        });

        eventSource.addEventListener('status_update', (event: any) => {
          try {
            const data = JSON.parse(event.data);
            onStatusUpdate(data.status);
          } catch (err) {
            console.error('[useSSE] Failed to parse status update:', err);
          }
        });

        eventSource.addEventListener('message_update', (event: any) => {
          try {
            const data = JSON.parse(event.data);
            onMessageUpdate(data.id, data.requires_user_input);
          } catch (err) {
            console.error('[useSSE] Failed to parse message update:', err);
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
            console.error('[useSSE] Failed to parse git diff update:', err);
          }
        });

        eventSource.addEventListener('error', (error: any) => {
          console.error('[useSSE] Connection error:', error);
          isConnectingRef.current = false;
          if (onError) {
            onError(new Error('SSE connection error'));
          }
        });
      } catch (error) {
        console.error('[useSSE] Failed to connect:', error);
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