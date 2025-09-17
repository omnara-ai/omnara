import React, { useState, useCallback } from 'react';
import {
  View,
  Text,
  StyleSheet,
  TouchableOpacity,
  ActivityIndicator,
  ScrollView,
} from 'react-native';
import { Plus, MoreVertical, Info } from 'lucide-react-native';
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

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.sectionTitle}>Agents</Text>
        <TouchableOpacity
          onPress={() => setIsAddAgentModalVisible(true)}
          activeOpacity={0.7}
        >
          <Plus size={20} color={theme.colors.primaryLight} strokeWidth={2} />
        </TouchableOpacity>
      </View>
      
      <View style={[
        styles.listContainer,
        userAgents.length > 4 && styles.listContainerScrollable
      ]}>
        <ScrollView
          showsVerticalScrollIndicator={false}
          scrollEnabled={userAgents.length > 4}
          nestedScrollEnabled={true}
        >
          {userAgents.map((agent, index) => {
            const hasWebhook = !!agent.webhook_type;
            const activeCount = agent.active_instance_count || 0;
            
            return (
              <TouchableOpacity
                key={agent.id}
                style={[
                  styles.agentRow,
                  index < userAgents.length - 1 && styles.agentRowBorder
                ]}
                onPress={() => handleAgentClick(agent)}
                activeOpacity={0.7}
              >
                <View style={styles.agentInfo}>
                  <Text style={styles.agentName}>{formatAgentTypeName(agent.name)}</Text>
                  <View style={styles.agentMeta}>
                    {activeCount > 0 ? (
                      <>
                        <View style={styles.activeDot} />
                        <Text style={styles.activeCount}>{activeCount} active</Text>
                      </>
                    ) : (
                      <Text style={styles.inactiveCount}>Inactive</Text>
                    )}
                  </View>
                </View>
                
                <View style={styles.actionButtons}>
                  {hasWebhook && (
                    <TouchableOpacity
                      onPress={(e) => handleLaunchAgent(agent, e)}
                      style={styles.launchButton}
                      activeOpacity={0.7}
                    >
                      <Plus size={14} color={theme.colors.white} strokeWidth={2.5} />
                      <Text style={styles.launchButtonText}>Launch</Text>
                    </TouchableOpacity>
                  )}
                  <TouchableOpacity
                    onPress={(e) => handleEditAgent(agent, e)}
                    style={styles.menuButton}
                    activeOpacity={0.7}
                  >
                    <MoreVertical size={16} color={theme.colors.white} strokeWidth={2} />
                  </TouchableOpacity>
                </View>
              </TouchableOpacity>
            );
          })}
        </ScrollView>
      </View>

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
    paddingHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.lg,
  },
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 12,
  },
  sectionTitle: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.text,
  },
  manageLink: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.primaryLight,
  },
  loadingContainer: {
    paddingVertical: theme.spacing.xl,
    alignItems: 'center',
  },
  listContainer: {
    backgroundColor: theme.colors.cardSurface,
    borderRadius: theme.borderRadius.sm,
    borderWidth: 1,
    borderColor: theme.colors.border,
  },
  listContainerScrollable: {
    maxHeight: 240, // Show ~4 items
  },
  agentRow: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: theme.spacing.md,
    paddingVertical: theme.spacing.md,
  },
  agentRowBorder: {
    borderBottomWidth: 1,
    borderBottomColor: theme.colors.borderDivider,
  },
  agentInfo: {
    flex: 1,
  },
  agentName: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.text,
    marginBottom: 2,
  },
  agentMeta: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.spacing.sm,
  },
  activeDot: {
    width: 6,
    height: 6,
    borderRadius: theme.borderRadius.full,
    backgroundColor: theme.colors.success,
  },
  activeCount: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.success,
  },
  inactiveCount: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
  actionButtons: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.spacing.xs,
  },
  launchButton: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    height: 28,
    paddingHorizontal: 10,
    borderRadius: 6,
    backgroundColor: theme.colors.primary,
    justifyContent: 'center',
  },
  launchButtonText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.white,
  },
  menuButton: {
    width: 28,
    height: 28,
    borderRadius: 6,
    justifyContent: 'center',
    alignItems: 'center',
  },
  // Empty state styles
  emptyStateContainer: {
    backgroundColor: theme.colors.cardSurface,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: theme.colors.border,
    padding: theme.spacing.lg,
    alignItems: 'center',
    marginTop: theme.spacing.lg,
  },
});