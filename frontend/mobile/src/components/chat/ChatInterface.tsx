import React, { useState, useEffect, useRef } from 'react';
import {
  View,
  ScrollView,
  Text,
  StyleSheet,
  KeyboardAvoidingView,
  Platform,
  Keyboard,
} from 'react-native';
import { theme } from '@/constants/theme';
import { ChatMessage, ChatMessageData, MessageGroup } from './ChatMessage';
import { ChatInput } from './ChatInput';
import { GitDiffPanel } from './GitDiffPanel';
import { BottomSheetDiffViewer } from './BottomSheetDiffViewer';
import { InstanceDetail, Message } from '@/types';

interface ChatInterfaceProps {
  instance: InstanceDetail;
  onMessageSubmit: (content: string) => Promise<void>;
  onLoadMoreMessages: (beforeMessageId: string) => Promise<Message[]>;
}


function groupMessages(messages: Message[], agentName: string): MessageGroup[] {
  const groups: MessageGroup[] = [];
  let currentGroup: MessageGroup | null = null;
  
  messages.forEach(message => {
    const isFromAgent = message.sender_type === 'AGENT';
    const sender = isFromAgent ? agentName : 'You';
    
    // Check if we should start a new group
    const shouldStartNewGroup = !currentGroup || 
      currentGroup.isFromAgent !== isFromAgent ||
      currentGroup.agentName !== sender ||
      // Start new group if messages are more than 5 minutes apart
      new Date(message.created_at).getTime() - new Date(currentGroup.timestamp).getTime() > 5 * 60 * 1000;
    
    if (shouldStartNewGroup) {
      currentGroup = {
        id: `group-${message.id}`,
        agentName: sender,
        timestamp: message.created_at,
        messages: [message],
        isFromAgent
      };
      groups.push(currentGroup);
    } else {
      currentGroup!.messages.push(message);
    }
  });
  
  return groups;
}

export const ChatInterface: React.FC<ChatInterfaceProps> = ({ 
  instance, 
  onMessageSubmit,
  onLoadMoreMessages
}) => {
  const [allMessages, setAllMessages] = useState<Message[]>([]);
  const [messageGroups, setMessageGroups] = useState<MessageGroup[]>([]);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [isLoadingMore, setIsLoadingMore] = useState(false);
  const [hasMoreMessages, setHasMoreMessages] = useState(true);
  const [isKeyboardVisible, setIsKeyboardVisible] = useState(false);
  const [showDiffViewer, setShowDiffViewer] = useState(false);
  const [selectedFileIndex, setSelectedFileIndex] = useState(0);
  const scrollViewRef = useRef<ScrollView>(null);
  const isAtBottomRef = useRef(true);
  const loadingRef = useRef(false);
  const previousContentHeight = useRef(0);
  const shouldRestoreScroll = useRef(false);

  // Merge instance messages (from SSE or initial load) with existing paginated messages
  useEffect(() => {
    setAllMessages(prev => {
      const messageMap = new Map<string, Message>();
      
      // Add existing paginated messages first
      prev.forEach(msg => messageMap.set(msg.id, msg));
      
      // Add/update with instance messages (from SSE or initial load)
      (instance.messages || []).forEach(msg => {
        messageMap.set(msg.id, msg);
      });
      
      // Convert to array and sort chronologically
      const combined = Array.from(messageMap.values());
      combined.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime());
      return combined;
    });
    
    // Determine if there are more messages to load based on initial load
    // If we get exactly our page size (50), there might be more
    if (instance.messages?.length === 50) {
      setHasMoreMessages(true);
    } else if ((instance.messages?.length || 0) < 50) {
      setHasMoreMessages(false);
    }
  }, [instance.messages]);
  
  // Group messages whenever allMessages changes
  useEffect(() => {
    const groups = groupMessages(allMessages, instance.agent_type?.name || 'Agent');
    setMessageGroups(groups);
  }, [allMessages, instance.agent_type?.name]);

  // Track keyboard visibility
  useEffect(() => {
    const keyboardWillShowListener = Keyboard.addListener(
      Platform.OS === 'ios' ? 'keyboardWillShow' : 'keyboardDidShow',
      () => {
        setIsKeyboardVisible(true);
        // Only scroll to bottom if already at bottom
        if (isAtBottomRef.current) {
          setTimeout(() => {
            scrollViewRef.current?.scrollToEnd({ animated: true });
          }, 50);
        }
      }
    );
    const keyboardWillHideListener = Keyboard.addListener(
      Platform.OS === 'ios' ? 'keyboardWillHide' : 'keyboardDidHide',
      () => setIsKeyboardVisible(false)
    );

    return () => {
      keyboardWillShowListener.remove();
      keyboardWillHideListener.remove();
    };
  }, []);

  // Smart scroll: only scroll to bottom if user was already at bottom
  // Use animated: false for initial load to avoid visible scrolling
  useEffect(() => {
    if (messageGroups.length > 0 && scrollViewRef.current && isAtBottomRef.current) {
      // Use requestAnimationFrame to ensure scroll happens after render
      requestAnimationFrame(() => {
        scrollViewRef.current?.scrollToEnd({ animated: false });
      });
    }
  }, [messageGroups]);
  
  // Handle scroll events to detect when to load more messages
  const handleScroll = async (event: any) => {
    const { layoutMeasurement, contentOffset, contentSize } = event.nativeEvent;
    const paddingToBottom = 20;
    isAtBottomRef.current = 
      layoutMeasurement.height + contentOffset.y >= contentSize.height - paddingToBottom;
    
    // Check if we should load more messages (scrolled to the top or bounce area)
    // On iOS, contentOffset.y can go negative due to bounce effect
    if (contentOffset.y <= 0 && !isLoadingMore && hasMoreMessages && allMessages.length > 0) {
      // Prevent concurrent requests
      if (loadingRef.current) return;
      loadingRef.current = true;
      
      setIsLoadingMore(true);
      
      // Get the oldest message ID for cursor-based pagination
      const oldestMessage = allMessages[0];
      if (oldestMessage) {
        // Save the current content height before loading
        const previousHeight = contentSize.height;
        
        try {
          // Load more messages
          const newMessages = await onLoadMoreMessages(oldestMessage.id);
          
          if (newMessages.length > 0) {
            // Merge new messages (avoiding duplicates)
            setAllMessages(prev => {
              const messageIds = new Set(prev.map(m => m.id));
              const uniqueNewMessages = newMessages.filter(m => !messageIds.has(m.id));
              
              // Prepend new messages and sort by created_at
              const combined = [...uniqueNewMessages, ...prev];
              combined.sort((a, b) => new Date(a.created_at).getTime() - new Date(b.created_at).getTime());
              return combined;
            });
            
            // Set up for scroll restoration
            previousContentHeight.current = previousHeight;
            shouldRestoreScroll.current = true;
            
            // Add a cooldown to prevent spam loading
            await new Promise(resolve => setTimeout(resolve, 800));
          } else {
            // No more messages to load
            setHasMoreMessages(false);
          }
        } finally {
          setIsLoadingMore(false);
          loadingRef.current = false;
        }
      }
    }
  };

  const handleSubmitMessage = async (content: string) => {
    setIsSubmitting(true);
    try {
      await onMessageSubmit(content);
    } finally {
      setIsSubmitting(false);
    }
  };

  // Check if we're waiting for user input and get the last message
  const isWaitingForInput = instance.status === 'AWAITING_INPUT';
  const lastMessage = allMessages.length > 0 
    ? allMessages[allMessages.length - 1]
    : null;
  const currentWaitingMessage = isWaitingForInput && lastMessage?.requires_user_input && lastMessage.sender_type === 'AGENT'
    ? lastMessage
    : null;

  const handleFilePress = (gitDiff: string, fileIndex: number) => {
    setSelectedFileIndex(fileIndex);
    setShowDiffViewer(true);
  };

  return (
    <KeyboardAvoidingView 
      style={styles.container}
      behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
    >
      {/* Chat Messages Area - takes all available space above input */}
      <ScrollView
        ref={scrollViewRef}
        style={styles.messagesContainer}
        contentContainerStyle={styles.messagesContent}
        showsVerticalScrollIndicator={false}
        onScroll={handleScroll}
        scrollEventThrottle={100}
        maintainVisibleContentPosition={{
          minIndexForVisible: 0,
          autoscrollToTopThreshold: 0
        }}
        onContentSizeChange={(width, height) => {
          // Handle scroll restoration after new messages are loaded
          if (shouldRestoreScroll.current && previousContentHeight.current > 0) {
            const heightDifference = height - previousContentHeight.current;
            
            if (heightDifference > 0) {
              // Scroll down by the height of the new content
              scrollViewRef.current?.scrollTo({
                y: heightDifference,
                animated: false
              });
            }
            
            // Reset the flag
            shouldRestoreScroll.current = false;
            previousContentHeight.current = 0;
          }
        }}
      >
        {/* Loading indicator at top */}
        {isLoadingMore && (
          <View style={styles.loadingContainer}>
            <Text style={styles.loadingText}>Loading more messages...</Text>
          </View>
        )}
        {messageGroups.length === 0 ? (
          <View style={styles.emptyContainer}>
            <Text style={styles.emptyText}>No activity yet.</Text>
            <Text style={styles.emptySubtext}>
              You can turn your phone off.{'\n'}Your agent will ping you when it needs you.
            </Text>
          </View>
        ) : (
          <View style={styles.messagesList}>
            {messageGroups.map((group, index) => {
              const isLastGroup = index === messageGroups.length - 1;
              const showWaitingIndicator = isLastGroup && isWaitingForInput;
              
              return (
                <ChatMessage 
                  key={group.id} 
                  messageGroup={group} 
                  showWaitingIndicator={showWaitingIndicator}
                />
              );
            })}
          </View>
        )}
      </ScrollView>

      {/* Bottom Section */}
      <View>
        {/* Git Diff Panel - hide when keyboard is visible */}
        {instance.git_diff && !isKeyboardVisible && (
          <GitDiffPanel 
            gitDiff={instance.git_diff} 
            hasInputBelow={instance.status !== 'COMPLETED'}
            isCompleted={instance.status === 'COMPLETED'}
            onFilePress={handleFilePress}
          />
        )}
        
        {/* Input Area */}
        {instance.status !== 'COMPLETED' && (
          <View style={[
            styles.bottomPanel,
            instance.git_diff && !isKeyboardVisible && styles.bottomPanelWithGitDiff,
            isKeyboardVisible && styles.bottomPanelKeyboardVisible
          ]}>
            <View style={[
              styles.inputSection,
              isKeyboardVisible && styles.inputSectionKeyboardVisible
            ]}>
              <ChatInput
                isWaitingForInput={isWaitingForInput}
                currentWaitingMessage={currentWaitingMessage}
                onMessageSubmit={handleSubmitMessage}
                isSubmitting={isSubmitting}
                hasGitDiff={!!instance.git_diff}
              />
            </View>
          </View>
        )}
      </View>
      
      {/* Bottom Sheet Diff Viewer - rendered at top level */}
      <BottomSheetDiffViewer
        visible={showDiffViewer}
        onClose={() => setShowDiffViewer(false)}
        gitDiff={instance.git_diff}
        initialFileIndex={selectedFileIndex}
      />
    </KeyboardAvoidingView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: theme.colors.background, // Deep midnight blue
  },
  messagesContainer: {
    flex: 1,
    backgroundColor: theme.colors.background, // Deep midnight blue
  },
  messagesContent: {
    paddingHorizontal: 0, // Remove horizontal padding, let message groups handle it
    paddingTop: theme.spacing.md,
    paddingBottom: theme.spacing.sm,
  },
  messagesList: {
    gap: theme.spacing.sm,
  },
  emptyContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    paddingVertical: theme.spacing.xl * 4,
  },
  emptyText: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.white,
    marginBottom: theme.spacing.sm,
  },
  emptySubtext: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
    textAlign: 'center',
  },
  bottomPanel: {
    marginHorizontal: theme.spacing.md,
    marginBottom: theme.spacing.lg,
  },
  bottomPanelWithGitDiff: {
    marginTop: 0,
  },
  inputSection: {
    paddingHorizontal: theme.spacing.md,
    paddingBottom: theme.spacing.md,
  },
  inputSectionKeyboardVisible: {
    paddingTop: theme.spacing.sm,
    paddingBottom: theme.spacing.sm,
  },
  bottomPanelKeyboardVisible: {
    marginBottom: 0,
  },
  loadingContainer: {
    paddingVertical: theme.spacing.md,
    alignItems: 'center',
  },
  loadingText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
});