import React, { useState } from 'react';
import {
  View,
  Text,
  StyleSheet,
  TouchableOpacity,
  ScrollView,
} from 'react-native';
import { Check, AlertCircle } from 'lucide-react-native';
import { theme } from '@/constants/theme';
import { AgentType, AgentInstance, AgentStatus } from '@/types';
import { useNavigation } from '@react-navigation/native';
import { formatAgentTypeName } from '@/utils/formatters';

interface StatusBarProps {
  agentTypes: AgentType[];
}

export const StatusBar: React.FC<StatusBarProps> = ({ agentTypes }) => {
  const navigation = useNavigation();

  // Collect all instances waiting for input
  const instancesWithQuestions = React.useMemo(() => {
    const instances: Array<AgentInstance & { typeName: string }> = [];
    
    agentTypes.forEach(type => {
      type.recent_instances.forEach(instance => {
        if (instance.status === AgentStatus.AWAITING_INPUT) {
          instances.push({
            ...instance,
            typeName: type.name,
          });
        }
      });
    });

    return instances;
  }, [agentTypes]);

  const hasQuestions = instancesWithQuestions.length > 0;

  const handleInstancePress = (instanceId: string) => {
    navigation.navigate('InstanceDetail', { instanceId });
  };

  if (hasQuestions) {
    return (
      <View style={styles.container}>
        <ScrollView
          horizontal
          showsHorizontalScrollIndicator={false}
          style={styles.questionsScrollView}
          contentContainerStyle={styles.questionsScrollContent}
        >
          {instancesWithQuestions.map((instance, index) => (
            <TouchableOpacity
              key={instance.id}
              style={[styles.questionCard, styles.warningCard]}
              onPress={() => handleInstancePress(instance.id)}
              activeOpacity={0.7}
            >
              <AlertCircle size={16} color={theme.colors.status.awaiting_input.text} strokeWidth={2} />
              <View style={styles.questionCardContent}>
                <Text style={styles.instanceNameWarning} numberOfLines={1} ellipsizeMode="tail">
                  {instance.name || formatAgentTypeName(instance.typeName)}
                </Text>
                <Text style={styles.instanceStepWarning} numberOfLines={1} ellipsizeMode="tail">
                  {instance.latest_message || 'Waiting for response...'}
                </Text>
              </View>
            </TouchableOpacity>
          ))}
        </ScrollView>
      </View>
    );
  }

  // If there are no agent types at all, don't show the status bar
  // This happens when the user hasn't configured any agents yet
  if (!agentTypes || agentTypes.length === 0) {
    return null;
  }

  // All clear status - only show if there are agents but no questions
  return (
    <View style={styles.container}>
      <View style={[styles.statusBar, styles.successBar]}>
        <View style={styles.statusContent}>
          <Check size={16} color={theme.colors.status.active.text} strokeWidth={2} />
          <Text style={styles.successText}>No agents require your input currently</Text>
        </View>
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    marginHorizontal: theme.spacing.lg,
    marginTop: theme.spacing.md,
    marginBottom: theme.spacing.lg,
  },
  statusBar: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: theme.spacing.md,
    paddingVertical: theme.spacing.sm - 2,
    borderRadius: theme.borderRadius.sm,
    borderWidth: 1,
  },
  successBar: {
    backgroundColor: theme.colors.status.active.bg,
    borderColor: theme.colors.status.active.border,
  },
  warningBar: {
    backgroundColor: theme.colors.status.awaiting_input.bg,
    borderColor: theme.colors.status.awaiting_input.border,
  },
  statusContent: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.spacing.sm,
  },
  successText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.status.active.text,
  },
  warningText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.status.awaiting_input.text,
  },
  questionsScrollView: {
    // Remove top margin since we don't have the header anymore
  },
  questionsScrollContent: {
    paddingRight: theme.spacing.lg,
  },
  questionCard: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: theme.spacing.lg,
    paddingVertical: theme.spacing.md,
    borderRadius: theme.borderRadius.sm,
    borderWidth: 1,
    marginRight: theme.spacing.sm,
    width: 320, // Longer fixed width
    gap: theme.spacing.sm,
  },
  warningCard: {
    backgroundColor: theme.colors.status.awaiting_input.bg,
    borderColor: theme.colors.status.awaiting_input.border,
  },
  questionCardContent: {
    flex: 1,
  },
  instanceNameWarning: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.status.awaiting_input.text,
    marginBottom: 2,
  },
  instanceStepWarning: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
});