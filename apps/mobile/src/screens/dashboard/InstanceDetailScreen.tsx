import React, { useCallback, useEffect, useState, useRef } from 'react';
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
import { InstanceDetail, AgentStatus, Message, InstanceShare, InstanceAccessLevel } from '@/types';
import { formatAgentTypeName } from '@/utils/formatters';
import { getStatusColor } from '@/utils/statusHelpers';
import { ChatInterface } from '@/components/chat/ChatInterface';
import { SSHInstancePanel } from '@/components/ssh';
import { useSSE } from '@/hooks/useSSE';
import { Share } from 'lucide-react-native';
import { ShareAccessModal } from '@/components/dashboard/ShareAccessModal';
import { reportError, reportMessage } from '@/lib/logger';

const resolveTransport = (metadata?: Record<string, unknown> | null): string | undefined => {
  if (!metadata || typeof metadata !== 'object') {
    return undefined;
  }
  const value = (metadata as Record<string, unknown>)['transport'];
  return typeof value === 'string' ? value : undefined;
};

export const InstanceDetailScreen: React.FC = () => {
  const route = useRoute();
  const navigation = useNavigation();
  const queryClient = useQueryClient();
  const { instanceId } = route.params as { instanceId: string };

  console.log('[InstanceDetailScreen] Component rendered - instanceId:', instanceId);

  const [instance, setInstance] = useState<InstanceDetail | null>(null);
  const [sseEnabled, setSseEnabled] = useState(false);
  const appStateRef = useRef<AppStateStatus>(AppState.currentState);

  const sentryTags = { feature: 'mobile-instance-detail', instanceId };

  const [shareModalVisible, setShareModalVisible] = useState(false);
  const [shares, setShares] = useState<InstanceShare[]>([]);
  const [sharesLoading, setSharesLoading] = useState(false);
  const [sharesLoaded, setSharesLoaded] = useState(false);
  const [shareEmail, setShareEmail] = useState('');
  const [shareAccess, setShareAccess] = useState<InstanceAccessLevel>(InstanceAccessLevel.WRITE);
  const [shareSubmitting, setShareSubmitting] = useState(false);
  const [shareError, setShareError] = useState<string | null>(null);

  const metadata = instance?.instance_metadata;
  const transport = resolveTransport(metadata);
  const isSSHInstance = transport === 'ssh';

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
    if (isSSHInstance) {
      return [];
    }

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

  const sortShares = useCallback((entries: InstanceShare[]) => {
    const owner = entries.find(entry => entry.is_owner);
    const others = entries.filter(entry => !entry.is_owner);
    return owner ? [owner, ...others] : others;
  }, []);

  const loadInstanceShares = useCallback(async () => {
    if (!instanceId) return;
    setSharesLoading(true);
    try {
      const data = await dashboardApi.getInstanceAccessList(instanceId);
      setShares(sortShares(data));
      setShareError(null);
      setSharesLoaded(true);
    } catch (err) {
      reportError(err, {
        context: 'Failed to load shared users',
        extras: { instanceId },
        tags: sentryTags,
      });
      setShareError(err instanceof Error ? err.message : 'Failed to load shared users');
    } finally {
      setSharesLoading(false);
    }
  }, [instanceId, sortShares]);

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

  useEffect(() => {
    setShareModalVisible(false);
    setShares([]);
    setSharesLoaded(false);
    setShareEmail('');
    setShareError(null);
    setShareAccess(InstanceAccessLevel.WRITE);
  }, [instance?.id]);

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
    enabled: !isSSHInstance && sseEnabled,
    onMessage: handleNewMessage,
    onStatusUpdate: handleStatusUpdate,
    onMessageUpdate: handleMessageUpdate,
    onGitDiffUpdate: handleGitDiffUpdate,
  });

  const handleOpenShareModal = useCallback(() => {
    if (!instance?.is_owner) return;
    setShareModalVisible(true);
    if (!sharesLoaded) {
      loadInstanceShares();
    }
  }, [instance?.is_owner, sharesLoaded, loadInstanceShares]);

  const handleCloseShareModal = useCallback(() => {
    setShareModalVisible(false);
    setShareError(null);
  }, []);

  const handleAddShare = useCallback(async () => {
    if (!instanceId || shareSubmitting) return;
    const email = shareEmail.trim();
    if (!email) return;

    setShareSubmitting(true);
    setShareError(null);
    try {
      const newShare = await dashboardApi.addInstanceShare(instanceId, {
        email,
        access: shareAccess,
      });
      setShares(prev => {
        const filtered = prev.filter(entry => entry.id !== newShare.id);
        return sortShares([...filtered, newShare]);
      });
      setShareEmail('');
    } catch (err) {
      reportError(err, {
        context: 'Failed to add share',
        extras: { instanceId, email, access: shareAccess },
        tags: sentryTags,
      });
      setShareError(err instanceof Error ? err.message : 'Failed to add share');
    } finally {
      setShareSubmitting(false);
    }
  }, [instanceId, shareAccess, shareEmail, shareSubmitting, sortShares]);

  const handleRemoveShare = useCallback(async (shareId: string) => {
    if (!instanceId) return;
    setShareError(null);
    try {
      await dashboardApi.removeInstanceShare(instanceId, shareId);
      setShares(prev => prev.filter(entry => entry.id !== shareId));
    } catch (err) {
      reportError(err, {
        context: 'Failed to remove share',
        extras: { instanceId, shareId },
        tags: sentryTags,
      });
      setShareError(err instanceof Error ? err.message : 'Failed to remove share');
    }
  }, [instanceId]);

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
      const transport = resolveTransport(freshData?.instance_metadata);
      if (
        freshData &&
        freshData.status !== AgentStatus.COMPLETED &&
        transport !== 'ssh'
      ) {
        console.log('[InstanceDetailScreen] Enabling SSE for active non-SSH instance');
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

  useEffect(() => {
    if (isSSHInstance) {
      setSseEnabled(false);
    }
  }, [isSSHInstance]);

  console.log('[InstanceDetailScreen] Query state - isLoading:', isLoading, 'instance:', !!instance, 'error:', !!error, 'sseEnabled:', sseEnabled);

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

  const shareButton = instance?.is_owner ? (
    <TouchableOpacity
      onPress={handleOpenShareModal}
      style={styles.shareButton}
      accessibilityRole="button"
      accessibilityLabel="Manage sharing"
      hitSlop={{ top: 8, bottom: 8, left: 8, right: 8 }}
    >
      <Share size={18} color={theme.colors.white} />
    </TouchableOpacity>
  ) : null;

  return (
    <View style={styles.container}>
      <SafeAreaView style={styles.safeArea} >
        {/* Header */}
        <Header 
          title="" 
          onBack={() => navigation.goBack()}
          centerContent={
            <View style={styles.titleContainer}>
              <View style={[styles.statusDot, { backgroundColor: getStatusColor(instance.status) }]} />
              <Text style={styles.title}>{instance.name || formatAgentTypeName(instance.agent_type_name || 'Agent')}</Text>
            </View>
          }
          rightContent={shareButton}
        />
        
        {isSSHInstance ? (
          <SSHInstancePanel instanceId={instance.id} />
        ) : (
          <ChatInterface
            key={instance.id} // Force React to create a new component instance for each chat
            instance={instance}
            onMessageSubmit={handleMessageSubmit}
            onLoadMoreMessages={loadMoreMessages}
          />
        )}
      </SafeAreaView>

      <ShareAccessModal
        visible={shareModalVisible}
        shares={shares}
        loading={sharesLoading}
        shareEmail={shareEmail}
        shareAccess={shareAccess}
        shareSubmitting={shareSubmitting}
        shareError={shareError}
        onClose={handleCloseShareModal}
        onEmailChange={setShareEmail}
        onAccessChange={setShareAccess}
        onAddShare={handleAddShare}
        onRemoveShare={handleRemoveShare}
      />
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
  titleContainer: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  statusDot: {
    width: 8,
    height: 8,
    borderRadius: 4,
    marginRight: theme.spacing.sm,
  },
  title: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
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
  shareButton: {
    paddingHorizontal: 0,
    paddingVertical: 0,
    backgroundColor: 'transparent',
  },
});
