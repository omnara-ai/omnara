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
import { formatAgentTypeName, formatTimeSince } from '@/utils/formatters';
import { getStatusColor, getStatusText } from '@/utils/statusHelpers';
import { Trash2, CheckCircle } from 'lucide-react-native';
import { useMutation, useQueryClient } from '@tanstack/react-query';
import { dashboardApi } from '@/services/api';

interface SwipeableInstanceCardProps {
  instance: AgentInstance;
  agentTypeName: string;
  onPress: () => void;
}

export const SwipeableInstanceCard: React.FC<SwipeableInstanceCardProps> = ({
  instance,
  agentTypeName,
  onPress,
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

  const handleMarkComplete = () => {
    if (instance.status === AgentStatus.COMPLETED) {
      Alert.alert('Already Complete', 'This instance is already marked as complete.');
      swipeableRef.current?.close();
      return;
    }
    
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

  // Handle rename
  const handleLongPress = () => {
    if (Platform.OS === 'ios') {
      // iOS has a native prompt
      Alert.prompt(
        'Rename Instance',
        'Enter a new name for this instance',
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
                  Alert.alert('Error', 'Failed to rename instance');
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
      Alert.alert(
        'Rename Instance',
        'Long press to rename is currently only supported on iOS. You can rename instances from the web dashboard.',
        [{ text: 'OK' }]
      );
    }
  };

  const renderLeftActions = (
    progress: Animated.AnimatedInterpolation<number>,
    dragX: Animated.AnimatedInterpolation<number>
  ) => {
    // Only show complete action if not already completed
    const isCompleted = [
      AgentStatus.COMPLETED,
      AgentStatus.FAILED,
      AgentStatus.KILLED
    ].includes(instance.status);
    
    if (isCompleted) {
      return null;
    }

    const translateX = dragX.interpolate({
      inputRange: [0, 80],
      outputRange: [-80, 0],
      extrapolate: 'clamp',
    });

    return (
      <Animated.View
        style={[
          styles.leftActionContainer,
          {
            transform: [{ translateX }],
          },
        ]}
      >
        <TouchableOpacity
          style={[styles.actionButton, styles.completeButton]}
          onPress={handleMarkComplete}
          activeOpacity={0.8}
        >
          <CheckCircle size={20} color={theme.colors.white} strokeWidth={2} />
          <Text style={styles.actionText}>Complete</Text>
        </TouchableOpacity>
      </Animated.View>
    );
  };

  const renderRightActions = (
    progress: Animated.AnimatedInterpolation<number>,
    dragX: Animated.AnimatedInterpolation<number>
  ) => {
    const translateX = dragX.interpolate({
      inputRange: [-80, 0],
      outputRange: [0, 80],
      extrapolate: 'clamp',
    });

    return (
      <Animated.View
        style={[
          styles.rightActionContainer,
          {
            transform: [{ translateX }],
          },
        ]}
      >
        <TouchableOpacity
          style={[styles.actionButton, styles.deleteButton]}
          onPress={handleDelete}
          activeOpacity={0.8}
        >
          <Trash2 size={20} color={theme.colors.white} strokeWidth={2} />
          <Text style={styles.actionText}>Delete</Text>
        </TouchableOpacity>
      </Animated.View>
    );
  };

  // Format agent type name for display
  const displayAgentTypeName = formatAgentTypeName(agentTypeName);

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
      <TouchableOpacity onPress={onPress} onLongPress={handleLongPress} activeOpacity={0.8}>
        <View style={styles.card}>
          <View style={styles.topRow}>
            <View style={styles.statusDot}>
              <View style={[
                styles.statusDotInner,
                { backgroundColor: getStatusColor(instance.status) }
              ]} />
            </View>
            <View style={styles.contentContainer}>
              <View style={styles.headerRow}>
                <Text style={styles.agentName} numberOfLines={2} ellipsizeMode="tail">
                  {instance.name || displayAgentTypeName}
                </Text>
                <Text style={styles.timestamp}>{formatTimeSince(instance.started_at)}</Text>
              </View>
              <Text style={styles.taskDescription} numberOfLines={2}>
                {instance.latest_message || 'No activity yet'}
              </Text>
            </View>
          </View>
        </View>
      </TouchableOpacity>
    </Swipeable>
  );
};

const styles = StyleSheet.create({
  card: {
    marginHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.md,
    backgroundColor: theme.colors.authContainer,
    borderRadius: theme.borderRadius.sm,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.08)',
    padding: theme.spacing.md,
  },
  topRow: {
    flexDirection: 'row',
    alignItems: 'center',
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
  headerRow: {
    position: 'relative',
    marginBottom: theme.spacing.xs,
    minHeight: 20, // Ensure minimum height for single line
  },
  agentName: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    paddingRight: 60, // Leave space for timestamp
    lineHeight: theme.fontSize.base * 1.2,
  },
  timestamp: {
    position: 'absolute',
    top: 0,
    right: 0,
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.5)',
  },
  taskDescription: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.7)',
    lineHeight: theme.fontSize.sm * 1.4,
    marginBottom: theme.spacing.sm,
  },
  bottomRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  statusText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: 'rgba(255, 255, 255, 0.6)',
  },
  metrics: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.spacing.md,
  },
  messageCount: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.5)',
  },
  leftActionContainer: {
    width: 80,
    marginRight: 0,
    marginBottom: theme.spacing.md,
    marginLeft: theme.spacing.lg,
  },
  rightActionContainer: {
    width: 80,
    marginLeft: 0,
    marginBottom: theme.spacing.md,
    marginRight: theme.spacing.lg,
  },
  actionButton: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    borderRadius: theme.borderRadius.sm,
  },
  completeButton: {
    backgroundColor: theme.colors.success,
  },
  deleteButton: {
    backgroundColor: theme.colors.error,
  },
  actionText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.white,
    marginTop: 4,
  },
});