import React, { useState, useCallback } from 'react';
import {
  View,
  Text,
  StyleSheet,
  TouchableOpacity,
  ActivityIndicator,
  FlatList,
} from 'react-native';
import { Plus, MoreVertical, Play } from 'lucide-react-native';
import { theme } from '@/constants/theme';
import { useNavigation, useFocusEffect } from '@react-navigation/native';
import { formatAgentTypeName } from '@/utils/formatters';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { dashboardApi } from '@/services/api';
import { LaunchAgentModal } from './LaunchAgentModal';
import { AgentConfigModal } from './AgentConfigModal';
import { DefaultOnboardingSteps as OnboardingStepsComponent } from '@/components/onboarding';  // Simplified

interface AgentsListProps {}

export const AgentsList: React.FC<AgentsListProps> = () => {
  const navigation = useNavigation();
  const queryClient = useQueryClient();
  const [selectedAgent, setSelectedAgent] = useState<any>(null);
  const [isLaunchModalVisible, setIsLaunchModalVisible] = useState(false);
  const [isConfigModalVisible, setIsConfigModalVisible] = useState(false);
  const [isAddAgentModalVisible, setIsAddAgentModalVisible] = useState(false);
  const [isScreenFocused, setIsScreenFocused] = useState(true);

  // Fetch user agents with polling
  const { data: userAgents, isLoading } = useQuery({
    queryKey: ['user-agents'],
    queryFn: () => dashboardApi.getUserAgents(),
    refetchInterval: isScreenFocused ? 5000 : false, // Poll every 5 seconds when focused
  });

  // Refresh on focus
  useFocusEffect(
    useCallback(() => {
      setIsScreenFocused(true);
      queryClient.invalidateQueries({ queryKey: ['user-agents'] });
      
      return () => {
        setIsScreenFocused(false);
      };
    }, [queryClient])
  );

  // Only show loading on initial load, not on refetch
  if (isLoading && !userAgents) {
    return (
      <View style={styles.loadingContainer}>
        <ActivityIndicator size="small" color={theme.colors.textMuted} />
      </View>
    );
  }

  if (!userAgents || userAgents.length === 0) {
    return (
      <View style={styles.container}>
        <View style={styles.emptyStateContainer}>
          <OnboardingStepsComponent />
        </View>
      </View>
    );
  }

  const handleAgentClick = (agent: any) => {
    (navigation as any).navigate('AllInstances', { 
      filterByAgentId: agent.id,
      agentName: agent.name 
    });
  };

  const handleLaunchAgent = (agent: any, e: any) => {
    e.stopPropagation();
    setSelectedAgent(agent);
    setIsLaunchModalVisible(true);
  };

  const handleEditAgent = (agent: any, e: any) => {
    e.stopPropagation();
    setSelectedAgent(agent);
    setIsConfigModalVisible(true);
  };

  const handleLaunchSuccess = (instanceId: string) => {
    (navigation as any).navigate('InstanceDetail', { instanceId });
  };

  const handleConfigSuccess = () => {
    queryClient.invalidateQueries({ queryKey: ['user-agents'] });
    queryClient.invalidateQueries({ queryKey: ['agent-types'] });
    setIsConfigModalVisible(false);
    setSelectedAgent(null);
  };

  const handleAddAgentSuccess = () => {
    queryClient.invalidateQueries({ queryKey: ['user-agents'] });
    queryClient.invalidateQueries({ queryKey: ['agent-types'] });
    setIsAddAgentModalVisible(false);
  };

  const renderAgentCard = ({ item: agent, index }) => {
    const hasWebhook = !!agent.webhook_type;
    const activeCount = agent.active_instance_count || 0;

    return (
      <TouchableOpacity
        style={styles.agentCard}
        onPress={() => handleAgentClick(agent)}
        activeOpacity={0.8}
      >
        <View style={styles.cardContent}>
          <View style={styles.cardHeader}>
            <View style={styles.agentTitleContainer}>
              <Text style={styles.agentName}>{formatAgentTypeName(agent.name)}</Text>
              {activeCount > 0 && (
                <View style={styles.statusContainer}>
                  <View style={styles.activeDot} />
                  <Text style={styles.activeCount}>{activeCount} active</Text>
                </View>
              )}
            </View>

            <View style={styles.actionButtons}>
              {hasWebhook && (
                <TouchableOpacity
                  onPress={(e) => handleLaunchAgent(agent, e)}
                  style={styles.launchIconButton}
                  activeOpacity={0.7}
                >
                  <Play size={16} color={theme.colors.white} strokeWidth={2} fill={theme.colors.white} />
                </TouchableOpacity>
              )}
              <TouchableOpacity
                onPress={(e) => handleEditAgent(agent, e)}
                style={styles.menuButton}
                activeOpacity={0.7}
              >
                <MoreVertical size={18} color={theme.colors.textMuted} strokeWidth={2} />
              </TouchableOpacity>
            </View>
          </View>
        </View>
      </TouchableOpacity>
    );
  };

  return (
    <View style={styles.container}>
      <FlatList
        data={userAgents}
        keyExtractor={(item) => item.id}
        renderItem={renderAgentCard}
        contentContainerStyle={styles.listContent}
        showsVerticalScrollIndicator={false}
        ListHeaderComponent={
          <View style={styles.header}>
            <TouchableOpacity
              onPress={() => setIsAddAgentModalVisible(true)}
              style={styles.addButton}
              activeOpacity={0.7}
            >
              <Plus size={18} color={theme.colors.white} strokeWidth={2.5} />
              <Text style={styles.addButtonText}>Add New Agent</Text>
            </TouchableOpacity>
          </View>
        }
      />

      <LaunchAgentModal
        visible={isLaunchModalVisible}
        onClose={() => {
          setIsLaunchModalVisible(false);
          setSelectedAgent(null);
        }}
        agent={selectedAgent}
        onLaunchSuccess={handleLaunchSuccess}
      />
      
      <AgentConfigModal
        visible={isConfigModalVisible}
        onClose={() => {
          setIsConfigModalVisible(false);
          setSelectedAgent(null);
        }}
        agent={selectedAgent}
        onSuccess={handleConfigSuccess}
      />
      
      <AgentConfigModal
        visible={isAddAgentModalVisible}
        onClose={() => setIsAddAgentModalVisible(false)}
        agent={null}
        onSuccess={handleAddAgentSuccess}
      />
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  header: {
    paddingHorizontal: theme.spacing.lg,
    paddingBottom: theme.spacing.lg,
    paddingTop: theme.spacing.md,
  },
  addButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    gap: theme.spacing.sm,
    backgroundColor: 'rgba(255, 140, 60, 0.25)',
    paddingHorizontal: theme.spacing.xl,
    paddingVertical: theme.spacing.md + 2,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: 'rgba(255, 140, 60, 0.45)',
    width: '100%',
    shadowColor: 'rgba(255, 140, 60, 0.5)',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.25,
    shadowRadius: 8,
    elevation: 3,
  },
  addButtonText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
  },
  listContent: {
    paddingBottom: theme.spacing.xl * 2,
  },
  agentCard: {
    marginHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.sm,
    backgroundColor: theme.colors.authContainer,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.1)',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.1,
    shadowRadius: 3,
    elevation: 2,
  },
  cardContent: {
    padding: theme.spacing.lg,
  },
  cardHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'flex-start',
  },
  agentTitleContainer: {
    flex: 1,
    marginRight: theme.spacing.md,
  },
  agentName: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.xs,
  },
  statusContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.spacing.xs,
  },
  activeDot: {
    width: 6,
    height: 6,
    borderRadius: theme.borderRadius.full,
    backgroundColor: theme.colors.success,
  },
  activeCount: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.success,
  },
  inactiveCount: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.textMuted,
  },
  actionButtons: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.spacing.xs,
  },
  launchIconButton: {
    width: 32,
    height: 32,
    borderRadius: theme.borderRadius.sm,
    backgroundColor: theme.colors.success,
    justifyContent: 'center',
    alignItems: 'center',
  },
  menuButton: {
    width: 32,
    height: 32,
    borderRadius: theme.borderRadius.sm,
    justifyContent: 'center',
    alignItems: 'center',
  },
  loadingContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    paddingVertical: theme.spacing.xl,
  },
  // Empty state styles
  emptyStateContainer: {
    backgroundColor: theme.colors.cardSurface,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: theme.colors.border,
    padding: theme.spacing.lg,
    alignItems: 'center',
    marginHorizontal: theme.spacing.lg,
    marginTop: theme.spacing.lg,
  },
});