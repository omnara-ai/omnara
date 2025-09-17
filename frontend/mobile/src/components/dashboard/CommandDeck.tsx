import React from 'react';
import {
  View,
  Text,
  StyleSheet,
  TouchableOpacity,
  ScrollView,
} from 'react-native';
import { Card } from '@/components/ui';
import { theme } from '@/constants/theme';
import { AgentType, AgentInstance, AgentStatus } from '@/types';
import { useNavigation } from '@react-navigation/native';

interface CommandDeckProps {
  agentTypes: AgentType[];
}

export const CommandDeck: React.FC<CommandDeckProps> = ({ agentTypes }) => {
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

  if (instancesWithQuestions.length === 0) return null;

  const handlePress = (instanceId: string) => {
    navigation.navigate('InstanceDetail', { instanceId });
  };

  // If there's only one agent with questions, show a full-width card
  if (instancesWithQuestions.length === 1) {
    const instance = instancesWithQuestions[0];
    
    return (
      <View style={styles.container}>
        <View style={styles.header}>
          <View style={styles.iconContainer}>
            <Text style={styles.icon}>👤</Text>
          </View>
          <View style={styles.titleContainer}>
            <Text style={styles.title}>Human Input Required</Text>
            <Text style={styles.subtitle}>
              1 agent waiting for your response
            </Text>
          </View>
        </View>

        <TouchableOpacity
          onPress={() => handlePress(instance.id)}
          activeOpacity={0.8}
        >
          <View style={styles.fullWidthCard}>
              <View style={styles.questionHeader}>
                <Text style={styles.agentName}>{instance.typeName}</Text>
                <View style={styles.questionBadge}>
                  <Text style={styles.questionCount}>!</Text>
                </View>
              </View>
              <Text style={styles.questionPreview} numberOfLines={2}>
                {instance.latest_message || 'Waiting for input...'}
              </Text>
              <Text style={styles.tapHint}>Tap to answer →</Text>
          </View>
        </TouchableOpacity>
      </View>
    );
  }

  // For multiple agents, show horizontal scrollable cards
  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <View style={styles.iconContainer}>
          <Text style={styles.icon}>👤</Text>
        </View>
        <View style={styles.titleContainer}>
          <Text style={styles.title}>Human Input Required</Text>
          <Text style={styles.subtitle}>
            {instancesWithQuestions.length} agents waiting for your response
          </Text>
        </View>
      </View>

      <ScrollView 
        horizontal 
        showsHorizontalScrollIndicator={false}
        contentContainerStyle={styles.scrollContent}
      >
        {instancesWithQuestions.map((instance) => (
          <TouchableOpacity
            key={instance.id}
            onPress={() => handlePress(instance.id)}
            activeOpacity={0.8}
          >
            <View style={styles.questionCard}>
                <View style={styles.questionHeader}>
                  <Text style={styles.agentName}>{instance.typeName}</Text>
                  <View style={styles.questionBadge}>
                    <Text style={styles.questionCount}>!</Text>
                  </View>
                </View>
                <Text style={styles.questionPreview} numberOfLines={2}>
                  {instance.latest_message || 'Waiting for input...'}
                </Text>
                <Text style={styles.tapHint}>Tap to answer →</Text>
            </View>
          </TouchableOpacity>
        ))}
      </ScrollView>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    marginBottom: theme.spacing.xl,
  },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.md,
  },
  iconContainer: {
    width: 48,
    height: 48,
    borderRadius: theme.borderRadius.full,
    backgroundColor: 'rgba(251, 191, 36, 0.15)',
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: theme.spacing.md,
  },
  icon: {
    fontSize: 24,
  },
  titleContainer: {
    flex: 1,
  },
  title: {
    fontSize: theme.fontSize.xl,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold,
    color: theme.colors.white,
    marginBottom: theme.spacing.xs / 2,
  },
  subtitle: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(251, 191, 36, 0.9)',
  },
  scrollContent: {
    paddingHorizontal: theme.spacing.lg,
  },
  questionCard: {
    width: 280,
    marginRight: theme.spacing.md,
    backgroundColor: theme.colors.authContainer,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: 'rgba(250, 204, 21, 0.3)',
    padding: theme.spacing.md,
  },
  fullWidthCard: {
    marginHorizontal: theme.spacing.lg,
    backgroundColor: theme.colors.authContainer,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: 'rgba(250, 204, 21, 0.3)',
    padding: theme.spacing.md,
  },
  questionHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: theme.spacing.sm,
  },
  agentName: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold,
    color: theme.colors.white,
  },
  questionBadge: {
    backgroundColor: 'rgba(251, 191, 36, 0.3)',
    paddingHorizontal: theme.spacing.sm,
    paddingVertical: theme.spacing.xs / 2,
    borderRadius: theme.borderRadius.full,
    minWidth: 24,
    alignItems: 'center',
  },
  questionCount: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold,
    color: 'rgba(251, 191, 36, 1)',
  },
  questionPreview: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.7)',
    marginBottom: theme.spacing.sm,
    lineHeight: theme.fontSize.sm * 1.4,
  },
  tapHint: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium,
    color: 'rgba(251, 191, 36, 0.9)',
  },
});