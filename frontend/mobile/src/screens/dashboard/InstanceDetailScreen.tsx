import React, { useCallback, useEffect, useState, useRef, useMemo } from 'react';
import {
  View,
  Text,
  StyleSheet,
  ActivityIndicator,
  TouchableOpacity,
  AppState,
  AppStateStatus,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useRoute, useNavigation, useFocusEffect } from '@react-navigation/native';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { theme } from '@/constants/theme';
import { Header } from '@/components/ui';
import { dashboardApi } from '@/services/api';
import { InstanceDetail, AgentStatus, Message } from '@/types';
import { formatAgentTypeName } from '@/utils/formatters';
import { withAlpha } from '@/lib/color';
import { Pause, AlertTriangle, Power, CheckCircle, AlertCircle } from 'lucide-react-native';
import { ChatInterface } from '@/components/chat/ChatInterface';
import { useSSE } from '@/hooks/useSSE';
import { reportError, reportMessage } from '@/lib/sentry';

export const InstanceDetailScreen: React.FC = () => {
  const route = useRoute();
  const navigation = useNavigation();
  const queryClient = useQueryClient();
  const { instanceId } = route.params as { instanceId: string };

  console.log('[InstanceDetailScreen] Component rendered - instanceId:', instanceId);

  const [instance, setInstance] = useState<InstanceDetail | null>(null);
  const [sseEnabled, setSseEnabled] = useState(false);
  const [now, setNow] = useState(() => Date.now());
  const appStateRef = useRef<AppStateStatus>(AppState.currentState);

  const sentryTags = { feature: 'mobile-instance-detail', instanceId };

  const { data: initialData, isLoading, error, refetch } = useQuery({
    queryKey: ['instance', instanceId],
    queryFn: () => {
      console.log('[InstanceDetailScreen] Fetching instance detail for:', instanceId);
      // Initially load only 50 most recent messages
      return dashboardApi.getInstanceDetail(instanceId, 50);
    },
    enabled: !!instanceId,
    retry: 2,
    staleTime: 0, // Always fetch fresh data
  });

  const loadMoreMessages = async (beforeMessageId: string): Promise<Message[]> => {
    if (!instanceId) return [];
    
    try {
      const messages = await dashboardApi.getInstanceMessages(instanceId, 50, beforeMessageId);
      return messages;
    } catch (err) {
      reportError(err, {
        context: 'Failed to load more messages',
        extras: { instanceId, beforeMessageId },
        tags: sentryTags,
      });
      return [];
    }
  };

  // Update local instance state when initial data loads
  useEffect(() => {
    if (initialData) {
      // Ensure messages array exists
      setInstance({
        ...initialData,
        messages: initialData.messages || []
      });
    }
  }, [initialData]);

  // Refresh the heartbeat comparison periodically so the status stays in sync
  useEffect(() => {
    const interval = setInterval(() => {
      setNow(Date.now());
    }, 15000);

    return () => clearInterval(interval);
  }, []);

  // SSE handlers
  const handleNewMessage = useCallback((message: Message) => {
    console.log('[InstanceDetailScreen] New message received:', message);
    setInstance(prev => {
      if (!prev) return prev;
      
      // Check if message already exists to prevent duplicates
      const messageExists = prev.messages.some(m => m.id === message.id);
      if (messageExists) {
        console.log('[InstanceDetailScreen] Message already exists, skipping');
        return prev;
      }
      
      return {
        ...prev,
        messages: [...(prev.messages || []), message],
        latest_message: message.content,
        latest_message_at: message.created_at,
        chat_length: (prev.chat_length || 0) + 1,
        status: message.requires_user_input ? AgentStatus.AWAITING_INPUT : prev.status,
      };
    });
  }, []);

  const handleStatusUpdate = useCallback((status: AgentStatus) => {
    console.log('[InstanceDetailScreen] Status update received:', status);
    setInstance(prev => {
      if (!prev) return prev;
      return {
        ...prev,
        status,
      };
    });
    
    // Disable SSE if status is completed
    if (status === AgentStatus.COMPLETED) {
      setSseEnabled(false);
    }
  }, []);

  const handleMessageUpdate = useCallback((messageId: string, requiresUserInput: boolean) => {
    console.log('[InstanceDetailScreen] Message update received:', messageId, requiresUserInput);
    setInstance(prev => {
      if (!prev) return prev;

      let matched = false;
      const messages = (prev.messages || []).map(msg => {
        if (msg.id === messageId) {
          matched = true;
          return { ...msg, requires_user_input: requiresUserInput };
        }
        return msg;
      });

      if (!matched) {
        reportMessage('Received message_update for unknown message', {
          context: 'Missing base message for SSE update',
          extras: { instanceId, messageId },
          tags: sentryTags,
        });
        return prev;
      }

      return {
        ...prev,
        messages,
      };
    });
  }, []);

  const handleGitDiffUpdate = useCallback((gitDiff: string | null) => {
    console.log('[InstanceDetailScreen] Git diff update received');
    setInstance(prev => {
      if (!prev) return prev;
      return {
        ...prev,
        git_diff: gitDiff,
      };
    });
  }, []);

  // Set up SSE connection
  useSSE({
    instanceId,
    enabled: sseEnabled,
    onMessage: handleNewMessage,
    onStatusUpdate: handleStatusUpdate,
    onMessageUpdate: handleMessageUpdate,
    onGitDiffUpdate: handleGitDiffUpdate,
  });

  // Refresh data and reconnect SSE
  const refreshAndReconnect = useCallback(() => {
    console.log('[InstanceDetailScreen] Refreshing data and reconnecting SSE');
    
    // First, disable SSE to ensure clean state
    setSseEnabled(false);
    
    // Refetch data
    refetch().then((result) => {
      console.log('[InstanceDetailScreen] Data refreshed, result:', result.data ? 'has data' : 'no data');
      
      // Use the fresh data from refetch result
      const freshData = result.data;
      if (freshData && freshData.status !== AgentStatus.COMPLETED) {
        console.log('[InstanceDetailScreen] Enabling SSE for active instance');
        setSseEnabled(true);
      }
    });
  }, [refetch]);

  // Handle app state changes (background/foreground)
  useEffect(() => {
    const handleAppStateChange = (nextAppState: AppStateStatus) => {
      console.log('[InstanceDetailScreen] App state changed from', appStateRef.current, 'to', nextAppState);
      
      if (appStateRef.current.match(/inactive|background/) && nextAppState === 'active') {
        console.log('[InstanceDetailScreen] App coming to foreground');
        // App is coming to foreground - refresh and reconnect
        refreshAndReconnect();
      }
      
      appStateRef.current = nextAppState;
    };

    const subscription = AppState.addEventListener('change', handleAppStateChange);

    return () => {
      subscription.remove();
    };
  }, [refreshAndReconnect]);

  // Handle screen focus (navigation between screens)
  useFocusEffect(
    useCallback(() => {
      console.log('[InstanceDetailScreen] Screen focused (navigation)');
      
      refreshAndReconnect();
      
      return () => {
        console.log('[InstanceDetailScreen] Screen lost focus, disabling SSE');
        // Disable SSE when navigating away
        setSseEnabled(false);
      };
    }, [refreshAndReconnect])
  );

  console.log('[InstanceDetailScreen] Query state - isLoading:', isLoading, 'instance:', !!instance, 'error:', !!error, 'sseEnabled:', sseEnabled);

  const presence = useMemo(() => {
    if (!instance) {
      return {
        label: 'Loading…',
        dotColor: theme.colors.textMuted,
        textColor: theme.colors.textMuted,
      };
    }

    const ttlSeconds = 60;
    const lastHeartbeat = instance.last_heartbeat_at ? new Date(instance.last_heartbeat_at).getTime() : null;
    const secondsSince = lastHeartbeat ? Math.floor((now - lastHeartbeat) / 1000) : null;
    const isOnline = secondsSince !== null && secondsSince <= ttlSeconds;

    if (!isOnline) {
      return {
        label: 'Offline',
        dotColor: theme.colors.error,
        textColor: theme.colors.error,
      };
    }

    switch (instance.status) {
      case AgentStatus.AWAITING_INPUT:
        return {
          label: 'Waiting',
          dotColor: theme.colors.warning,
          textColor: theme.colors.warning,
        };
      case AgentStatus.PAUSED:
        return {
          label: 'Paused',
          dotColor: theme.colors.info,
          textColor: theme.colors.info,
        };
      case AgentStatus.FAILED:
        return {
          label: 'Error',
          dotColor: theme.colors.error,
          textColor: theme.colors.error,
        };
      case AgentStatus.KILLED:
        return {
          label: 'Stopped',
          dotColor: theme.colors.error,
          textColor: theme.colors.error,
        };
      case AgentStatus.COMPLETED:
        return {
          label: 'Done',
          dotColor: theme.colors.textMuted,
          textColor: theme.colors.textMuted,
        };
      default:
        return {
          label: 'Active',
          dotColor: theme.colors.success,
          textColor: theme.colors.success,
        };
    }
  }, [instance, now]);

  if (isLoading) {
    console.log('[InstanceDetailScreen] Showing loading state');
    return (
      <View style={styles.container}>
        <SafeAreaView style={styles.safeArea} >
          <Header title="" onBack={() => navigation.goBack()} />
          <View style={styles.loadingContainer}>
            <ActivityIndicator size="large" color={theme.colors.primary} />
            <Text style={styles.loadingText}>Loading instance details...</Text>
          </View>
        </SafeAreaView>
      </View>
    );
  }

  if (error) {
    reportError(error, {
      context: '[InstanceDetailScreen] Error loading instance',
      extras: { instanceId },
      tags: sentryTags,
    });
    return (
      <View style={styles.container}>
        <SafeAreaView style={styles.safeArea} >
          <Header title="" onBack={() => navigation.goBack()} />
          <View style={styles.errorContainer}>
            <Text style={styles.errorText}>Error loading instance</Text>
            <Text style={styles.errorDetail}>{(error as Error).message}</Text>
            <TouchableOpacity 
              style={styles.retryButton}
              onPress={() => refetch()}
            >
              <Text style={styles.retryText}>Try Again</Text>
            </TouchableOpacity>
          </View>
        </SafeAreaView>
      </View>
    );
  }

  if (!instance) {
    console.log('[InstanceDetailScreen] Instance not found');
    return (
      <View style={styles.container}>
        <SafeAreaView style={styles.safeArea} >
          <Header title="" onBack={() => navigation.goBack()} />
          <View style={styles.errorContainer}>
            <Text style={styles.errorText}>Instance not found</Text>
          </View>
        </SafeAreaView>
      </View>
    );
  }

  console.log('[InstanceDetailScreen] Rendering instance detail - status:', instance.status, 'messages:', instance.messages?.length || 0);

  const handleMessageSubmit = async (content: string) => {
    if (!instanceId) return;
    
    try {
      await dashboardApi.submitUserMessage(instanceId, content);
      // No need to refresh - SSE will push the new message
    } catch (err) {
      reportError(err, {
        context: 'Failed to submit user message',
        extras: { instanceId },
        tags: sentryTags,
      });
      throw err; // Re-throw for ChatInterface to handle
    }
  };

  return (
    <View style={styles.container}>
      <SafeAreaView style={styles.safeArea} >
        {/* Header */}
        <Header
          title={instance.name || formatAgentTypeName(instance.agent_type_name || 'Agent')}
          onBack={() => navigation.goBack()}
          rightContent={
            <View style={styles.statusIndicator}>
              <View style={[styles.statusDot, { backgroundColor: presence.dotColor }]} />
              <Text style={[styles.statusText, { color: presence.textColor }]}>
                {presence.label}
              </Text>
            </View>
          }
        />
        
        {/* Chat Interface */}
        <ChatInterface
          key={instance.id} // Force React to create a new component instance for each chat
          instance={instance}
          onMessageSubmit={handleMessageSubmit}
          onLoadMoreMessages={loadMoreMessages}
        />
      </SafeAreaView>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: theme.colors.background, // Deep midnight blue
  },
  safeArea: {
    flex: 1,
  },
  statusIndicator: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
  },
  statusDot: {
    width: 8,
    height: 8,
    borderRadius: 4,
  },
  statusText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
  },
  loadingContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
  },
  loadingText: {
    marginTop: theme.spacing.md,
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
  errorContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: theme.spacing.lg,
  },
  errorText: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.sm,
  },
  errorDetail: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
    textAlign: 'center',
    marginBottom: theme.spacing.xl,
  },
  retryButton: {
    backgroundColor: theme.colors.primary,
    paddingHorizontal: theme.spacing.lg,
    paddingVertical: theme.spacing.md,
    borderRadius: theme.borderRadius.md,
  },
  retryText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
  },
});
