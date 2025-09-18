import React, { useCallback, useEffect, useState, useRef } from 'react';
import {
  View,
  Text,
  StyleSheet,
  ActivityIndicator,
  TouchableOpacity,
  AppState,
  AppStateStatus,
  Modal,
  TextInput,
  FlatList,
  KeyboardAvoidingView,
  Platform,
  TouchableWithoutFeedback,
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
import { useSSE } from '@/hooks/useSSE';
import { reportError, reportMessage } from '@/lib/sentry';
import { Share, X, ChevronDown } from 'lucide-react-native';

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
  const [shareAccessMenuVisible, setShareAccessMenuVisible] = useState(false);

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
    setShareAccessMenuVisible(false);
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
    enabled: sseEnabled,
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
    setShareAccessMenuVisible(false);
  }, []);

  const handleAddShare = useCallback(async () => {
    if (!instanceId || shareSubmitting) return;
    const email = shareEmail.trim();
    if (!email) return;

    setShareSubmitting(true);
    setShareError(null);
    setShareAccessMenuVisible(false);
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

  const renderShareItem = useCallback(({ item }: { item: InstanceShare }) => (
    <View style={styles.shareItem}>
      <View style={styles.shareItemText}>
        <Text style={styles.shareItemEmail} numberOfLines={1} ellipsizeMode="tail">
          {item.email}
          {item.is_owner ? ' (Owner)' : ''}
        </Text>
        <Text style={styles.shareItemMeta} numberOfLines={1} ellipsizeMode="tail">
          {item.display_name ? `${item.display_name} • ` : ''}
          {item.access === InstanceAccessLevel.WRITE ? 'Write access' : 'Read access'}
          {item.invited ? ' • Pending' : ''}
        </Text>
      </View>
      {!item.is_owner && (
        <TouchableOpacity
          onPress={() => handleRemoveShare(item.id)}
          style={styles.shareRemoveButton}
        >
          <Text style={styles.shareRemoveText}>Remove</Text>
        </TouchableOpacity>
      )}
    </View>
  ), [handleRemoveShare]);

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
        
        {/* Chat Interface */}
        <ChatInterface
          key={instance.id} // Force React to create a new component instance for each chat
          instance={instance}
          onMessageSubmit={handleMessageSubmit}
          onLoadMoreMessages={loadMoreMessages}
        />
      </SafeAreaView>

      <Modal
        visible={shareModalVisible}
        transparent
        animationType="fade"
        onRequestClose={handleCloseShareModal}
      >
        <View style={styles.shareModalBackdrop}>
          <TouchableWithoutFeedback onPress={handleCloseShareModal}>
            <View style={styles.shareModalBackdropTouchable} />
          </TouchableWithoutFeedback>

          <KeyboardAvoidingView
            behavior={Platform.OS === 'ios' ? 'padding' : undefined}
            style={styles.shareModalWrapper}
          >
            <TouchableWithoutFeedback onPress={() => {}}>
              <View style={styles.shareModalContainer}>
                <View style={styles.shareModalHeader}>
                  <Text style={styles.shareModalTitle}>Shared Access</Text>
                  <TouchableOpacity
                    onPress={handleCloseShareModal}
                    style={styles.shareModalCloseButton}
                    accessibilityRole="button"
                    accessibilityLabel="Close sharing settings"
                  >
                    <X size={16} color={theme.colors.textMuted} />
                  </TouchableOpacity>
                </View>
                <Text style={styles.shareModalSubtitle}>
                  Manage who can view or edit this session.
                </Text>

                <View style={styles.shareListContainer}>
                  {sharesLoading ? (
                    <ActivityIndicator size="small" color={theme.colors.primary} />
                  ) : shares.length === 0 ? (
                    <Text style={styles.shareEmptyText}>No one else has access yet.</Text>
                  ) : (
                    <FlatList
                      data={shares}
                      keyExtractor={item => item.id}
                      renderItem={renderShareItem}
                      contentContainerStyle={styles.shareListContent}
                      ItemSeparatorComponent={() => <View style={styles.shareItemSeparator} />}
                      keyboardShouldPersistTaps="handled"
                    />
                  )}
                </View>

                <View style={styles.shareForm}>
                  <Text style={styles.shareFormLabel}>Invite by email</Text>
                  <TextInput
                    value={shareEmail}
                    onChangeText={setShareEmail}
                    placeholder="user@example.com"
                    placeholderTextColor={theme.colors.textMuted}
                    keyboardType="email-address"
                    autoCapitalize="none"
                    autoCorrect={false}
                    style={styles.shareInput}
                  />

                  <Text style={styles.shareFormLabel}>Access level</Text>
                  <View style={styles.shareAccessSelectWrapper}>
                    <TouchableOpacity
                      onPress={() => setShareAccessMenuVisible(prev => !prev)}
                      style={styles.shareAccessSelect}
                      activeOpacity={0.8}
                    >
                      <Text style={styles.shareAccessSelectText}>
                        {shareAccess === InstanceAccessLevel.WRITE ? 'Write access' : 'Read access'}
                      </Text>
                      <ChevronDown size={16} color={theme.colors.textMuted} />
                    </TouchableOpacity>
                    {shareAccessMenuVisible && (
                      <View style={styles.shareAccessDropdown}>
                        {[InstanceAccessLevel.WRITE, InstanceAccessLevel.READ].map(level => (
                          <TouchableOpacity
                            key={level}
                            onPress={() => {
                              setShareAccess(level);
                              setShareAccessMenuVisible(false);
                            }}
                            style={[
                              styles.shareAccessDropdownItem,
                              shareAccess === level && styles.shareAccessDropdownItemActive,
                            ]}
                          >
                            <Text
                              style={[
                                styles.shareAccessDropdownText,
                                shareAccess === level && styles.shareAccessDropdownTextActive,
                              ]}
                            >
                              {level === InstanceAccessLevel.WRITE ? 'Write access' : 'Read access'}
                            </Text>
                          </TouchableOpacity>
                        ))}
                      </View>
                    )}
                  </View>

                  {shareError && (
                    <Text style={styles.shareErrorText}>{shareError}</Text>
                  )}

                  <TouchableOpacity
                    onPress={handleAddShare}
                    style={styles.shareSubmitButton}
                    disabled={shareSubmitting}
                  >
                    <Text style={styles.shareSubmitText}>
                      {shareSubmitting ? 'Adding…' : 'Add'}
                    </Text>
                  </TouchableOpacity>
                </View>
              </View>
            </TouchableWithoutFeedback>
          </KeyboardAvoidingView>
        </View>
      </Modal>
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
  shareModalBackdrop: {
    flex: 1,
    backgroundColor: 'rgba(0, 0, 0, 0.6)',
    justifyContent: 'center',
    paddingHorizontal: theme.spacing.lg,
  },
  shareModalBackdropTouchable: {
    ...StyleSheet.absoluteFillObject,
  },
  shareModalWrapper: {
    flex: 1,
    justifyContent: 'center',
  },
  shareModalContainer: {
    backgroundColor: theme.colors.cardSurface,
    borderRadius: theme.borderRadius.xl,
    padding: theme.spacing.lg,
    borderWidth: 1,
    borderColor: theme.colors.border,
    gap: theme.spacing.md,
  },
  shareModalHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  shareModalTitle: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
  },
  shareModalCloseButton: {
    width: 28,
    height: 28,
    borderRadius: 14,
    backgroundColor: theme.colors.panelSurface,
    justifyContent: 'center',
    alignItems: 'center',
  },
  shareModalSubtitle: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
  shareListContainer: {
    maxHeight: 220,
  },
  shareEmptyText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
  shareListContent: {
    gap: theme.spacing.sm,
  },
  shareItemSeparator: {
    height: theme.spacing.xs,
  },
  shareItem: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    borderWidth: 1,
    borderColor: theme.colors.borderDivider,
    borderRadius: theme.borderRadius.lg,
    paddingVertical: theme.spacing.sm,
    paddingHorizontal: theme.spacing.md,
    backgroundColor: theme.colors.panelSurface,
  },
  shareItemText: {
    flex: 1,
    marginRight: theme.spacing.sm,
  },
  shareItemEmail: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.white,
  },
  shareItemMeta: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
  shareRemoveButton: {
    paddingHorizontal: theme.spacing.sm,
    paddingVertical: theme.spacing.xs,
  },
  shareRemoveText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.medium,
    color: theme.colors.error,
  },
  shareForm: {
    gap: theme.spacing.sm,
  },
  shareFormLabel: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.textMuted,
    textTransform: 'uppercase',
    letterSpacing: 0.6,
  },
  shareInput: {
    borderWidth: 1,
    borderColor: theme.colors.borderDivider,
    borderRadius: theme.borderRadius.md,
    paddingHorizontal: theme.spacing.md,
    paddingVertical: theme.spacing.sm,
    color: theme.colors.white,
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    backgroundColor: theme.colors.background,
  },
  shareAccessSelectWrapper: {
    position: 'relative',
    zIndex: 10,
    marginBottom: theme.spacing.sm,
  },
  shareAccessSelect: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    borderWidth: 1,
    borderColor: theme.colors.borderDivider,
    borderRadius: theme.borderRadius.md,
    paddingHorizontal: theme.spacing.md,
    paddingVertical: theme.spacing.sm,
    backgroundColor: theme.colors.background,
  },
  shareAccessSelectText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.white,
  },
  shareAccessDropdown: {
    position: 'absolute',
    top: '110%',
    left: 0,
    right: 0,
    borderRadius: theme.borderRadius.md,
    borderWidth: 1,
    borderColor: theme.colors.borderDivider,
    backgroundColor: theme.colors.cardSurface,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.2,
    shadowRadius: 6,
    elevation: 6,
    zIndex: 20,
  },
  shareAccessDropdownItem: {
    paddingHorizontal: theme.spacing.md,
    paddingVertical: theme.spacing.sm,
  },
  shareAccessDropdownItemActive: {
    backgroundColor: theme.colors.borderLight,
  },
  shareAccessDropdownText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
  shareAccessDropdownTextActive: {
    color: theme.colors.white,
  },
  shareErrorText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.status.failed,
    marginTop: theme.spacing.xs,
  },
  shareSubmitButton: {
    backgroundColor: '#d97706',
    borderRadius: theme.borderRadius.md,
    paddingVertical: theme.spacing.sm,
    alignItems: 'center',
    marginTop: theme.spacing.md,
  },
  shareSubmitText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
  },
});
