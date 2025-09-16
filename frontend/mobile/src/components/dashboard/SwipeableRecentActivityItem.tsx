import React, { useRef } from 'react';
import {
  View,
  Text,
  StyleSheet,
  TouchableOpacity,
  Animated,
  Alert,
  Platform,
} from 'react-native';
import { Swipeable } from 'react-native-gesture-handler';
import { theme } from '@/constants/theme';
import { AgentInstance, AgentStatus } from '@/types';
import { formatAgentTypeName } from '@/utils/formatters';
import { getStatusColor } from '@/utils/statusHelpers';
import { Trash2, CheckCircle } from 'lucide-react-native';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { dashboardApi } from '@/services/api';

interface SwipeableRecentActivityItemProps {
  instance: AgentInstance;
  typeName: string;
  timestamp: string;
  onPress: () => void;
  isLast: boolean;
}

export const SwipeableRecentActivityItem: React.FC<SwipeableRecentActivityItemProps> = ({
  instance,
  typeName,
  timestamp,
  onPress,
  isLast,
}) => {
  const swipeableRef = useRef<Swipeable>(null);
  const queryClient = useQueryClient();
  
  // Mutation for marking complete
  const markCompleteMutation = useMutation({
    mutationFn: () => dashboardApi.markInstanceComplete(instance.id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['agent-types'] });
      swipeableRef.current?.close();
    },
    onError: (error: any) => {
      Alert.alert('Error', error.message || 'Failed to mark as complete');
    },
  });

  // Mutation for deleting
  const deleteMutation = useMutation({
    mutationFn: () => dashboardApi.deleteInstance(instance.id),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['agent-types'] });
    },
    onError: (error: any) => {
      Alert.alert('Error', error.message || 'Failed to delete instance');
    },
  });

  // Handle rename
  const handleLongPress = () => {
    if (Platform.OS === 'ios') {
      // iOS has a native prompt
      Alert.prompt(
        'Rename Chat',
        'Enter a new name for this chat',
        [
          {
            text: 'Cancel',
            style: 'cancel',
          },
          {
            text: 'Rename',
            onPress: async (newName) => {
              if (newName && newName.trim() && newName !== instance.name) {
                try {
                  await dashboardApi.updateAgentInstance(instance.id, { name: newName.trim() });
                  queryClient.invalidateQueries({ queryKey: ['agent-types'] });
                } catch (error) {
                  Alert.alert('Error', 'Failed to rename chat');
                }
              }
            },
          },
        ],
        'plain-text',
        instance.name || ''
      );
    } else {
      // Android doesn't have Alert.prompt, so we'll use a simple alert for now
      // In production, you might want to use a third-party library or custom modal
      Alert.alert(
        'Rename Chat',
        'Long press to rename is currently only supported on iOS. You can rename chats from the web dashboard.',
        [{ text: 'OK' }]
      );
    }
  };

  const handleMarkComplete = () => {
    Alert.alert(
      'Mark Complete',
      'Are you sure you want to mark this instance as complete?',
      [
        {
          text: 'Cancel',
          style: 'cancel',
          onPress: () => swipeableRef.current?.close(),
        },
        {
          text: 'Complete',
          style: 'default',
          onPress: () => markCompleteMutation.mutate(),
        },
      ]
    );
  };

  const handleDelete = () => {
    Alert.alert(
      'Delete Instance',
      'Are you sure you want to delete this instance? This action cannot be undone.',
      [
        {
          text: 'Cancel',
          style: 'cancel',
          onPress: () => swipeableRef.current?.close(),
        },
        {
          text: 'Delete',
          style: 'destructive',
          onPress: () => deleteMutation.mutate(),
        },
      ]
    );
  };

  const renderLeftActions = () => {
    // Only show complete action if not already completed
    const isCompleted = [
      AgentStatus.COMPLETED,
      AgentStatus.FAILED,
      AgentStatus.KILLED
    ].includes(instance.status);
    
    if (isCompleted) {
      return null;
    }

    return (
      <View style={styles.leftActionContainer}>
        <TouchableOpacity
          style={[styles.actionButton, styles.completeButton]}
          onPress={handleMarkComplete}
          activeOpacity={0.8}
        >
          <CheckCircle size={18} color={theme.colors.white} strokeWidth={2} />
        </TouchableOpacity>
      </View>
    );
  };

  const renderRightActions = () => {
    return (
      <View style={styles.rightActionContainer}>
        <TouchableOpacity
          style={[styles.actionButton, styles.deleteButton]}
          onPress={handleDelete}
          activeOpacity={0.8}
        >
          <Trash2 size={18} color={theme.colors.white} strokeWidth={2} />
        </TouchableOpacity>
      </View>
    );
  };

  const getActivityText = () => {
    // Always show the latest message if available
    if (instance.latest_message) {
      return instance.latest_message;
    }
    
    // Fall back to status-specific messages only if no message exists
    switch (instance.status) {
      case AgentStatus.ACTIVE:
        return 'Processing...';
      case AgentStatus.AWAITING_INPUT:
        return 'Waiting for your response';
      case AgentStatus.COMPLETED:
        return 'Task completed successfully';
      case AgentStatus.FAILED:
        return 'Task failed';
      case AgentStatus.KILLED:
        return 'Task terminated by user';
      case AgentStatus.PAUSED:
        return 'Task paused by user';
      default:
        return 'Status unknown';
    }
  };

  const formatTime = (timestamp: string) => {
    const date = new Date(timestamp);
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffMins = Math.floor(diffMs / 60000);
    
    if (diffMins < 1) return 'just now';
    if (diffMins < 60) return `${diffMins}m ago`;
    
    const diffHours = Math.floor(diffMins / 60);
    if (diffHours < 24) return `${diffHours}h ago`;
    
    const diffDays = Math.floor(diffHours / 24);
    return `${diffDays}d ago`;
  };

  return (
    <Swipeable
      ref={swipeableRef}
      renderLeftActions={renderLeftActions}
      renderRightActions={renderRightActions}
      overshootLeft={false}
      overshootRight={false}
      friction={2}
      leftThreshold={40}
      rightThreshold={40}
    >
      <TouchableOpacity
        onPress={onPress}
        onLongPress={handleLongPress}
        activeOpacity={0.8}
      >
        <View style={[
          styles.activityItem,
          !isLast && styles.activityItemBorder
        ]}>
          <View style={styles.statusDot}>
            <View style={[
              styles.statusDotInner,
              { backgroundColor: getStatusColor(instance.status) }
            ]} />
          </View>
          <View style={styles.contentContainer}>
            <View style={styles.itemHeader}>
              <Text style={styles.agentName}>{instance.name || formatAgentTypeName(typeName)}</Text>
              <Text style={styles.timestamp}>{formatTime(timestamp)}</Text>
            </View>
            <Text style={styles.activityText} numberOfLines={1}>
              {getActivityText()}
            </Text>
          </View>
        </View>
      </TouchableOpacity>
    </Swipeable>
  );
};

const styles = StyleSheet.create({
  activityItem: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: theme.spacing.sm + 2,
    paddingHorizontal: theme.spacing.md,
    backgroundColor: theme.colors.authContainer,
  },
  activityItemBorder: {
    borderBottomWidth: 1,
    borderBottomColor: 'rgba(255, 255, 255, 0.05)',
  },
  statusDot: {
    width: 24,
    height: 24,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: theme.spacing.md,
  },
  statusDotInner: {
    width: 8,
    height: 8,
    borderRadius: theme.borderRadius.full,
  },
  contentContainer: {
    flex: 1,
  },
  itemHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: theme.spacing.xs / 2,
  },
  agentName: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
  },
  timestamp: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.5)',
  },
  activityText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.7)',
  },
  leftActionContainer: {
    width: 60,
    justifyContent: 'center',
    alignItems: 'center',
    backgroundColor: theme.colors.success,
  },
  rightActionContainer: {
    width: 60,
    justifyContent: 'center',
    alignItems: 'center',
    backgroundColor: theme.colors.error,
  },
  actionButton: {
    flex: 1,
    width: '100%',
    justifyContent: 'center',
    alignItems: 'center',
  },
  completeButton: {
    backgroundColor: theme.colors.success,
  },
  deleteButton: {
    backgroundColor: theme.colors.error,
  },
});