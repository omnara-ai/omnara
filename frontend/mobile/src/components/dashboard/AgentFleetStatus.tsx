import React, { useState, useEffect } from 'react';
import {
  View,
  Text,
  StyleSheet,
  TouchableOpacity,
  ScrollView,
} from 'react-native';
import { Bot, Terminal, Code, Cpu, Plus, Settings } from 'lucide-react-native';
import { Card, Button } from '@/components/ui';
import { theme } from '@/constants/theme';
import { withAlpha } from '@/lib/color';
import { AgentType, AgentStatus } from '@/types';
import { useNavigation } from '@react-navigation/native';
import { formatAgentTypeName } from '@/utils/formatters';
import { useQuery } from '@tanstack/react-query';
import { dashboardApi } from '@/services/api';
import { LaunchAgentModal } from './LaunchAgentModal';

interface AgentFleetStatusProps {}

export const AgentFleetStatus: React.FC<AgentFleetStatusProps> = () => {
  const navigation = useNavigation();
  const [selectedAgent, setSelectedAgent] = useState<any>(null);
  const [isModalVisible, setIsModalVisible] = useState(false);

  // Fetch user agents
  const { data: userAgents, isLoading } = useQuery({
    queryKey: ['user-agents'],
    queryFn: () => dashboardApi.getUserAgents(),
  });

  // Show all user agents
  const agentsToShow = userAgents || [];

  if (isLoading) {
    return (
      <View style={styles.loadingContainer}>
        <Text style={styles.loadingText}>Loading agents...</Text>
      </View>
    );
  }

  if (!userAgents || userAgents.length === 0) {
    return null;
  }

  const handleAgentClick = (agent: any) => {
    (navigation as any).navigate('Instances', { filterByAgentId: agent.id });
  };

  const handleLaunchAgent = (agent: any, e: any) => {
    e.stopPropagation();
    setSelectedAgent(agent);
    setIsModalVisible(true);
  };

  const handleLaunchSuccess = (instanceId: string) => {
    // Navigate to the new instance
    (navigation as any).navigate('InstanceDetail', { instanceId });
  };

  const getAgentIcon = (agentName: string) => {
    const name = agentName.toLowerCase();
    if (name.includes('claude')) {
      return <Bot size={32} color={theme.colors.white} strokeWidth={1.5} />;
    } else if (name.includes('cursor')) {
      return <Code size={32} color={theme.colors.white} strokeWidth={1.5} />;
    } else if (name.includes('terminal') || name.includes('bash')) {
      return <Terminal size={32} color={theme.colors.white} strokeWidth={1.5} />;
    } else {
      return <Cpu size={32} color={theme.colors.white} strokeWidth={1.5} />;
    }
  };

  const getStatusPill = (status: AgentStatus, count: number) => {
    const statusConfig = {
      [AgentStatus.ACTIVE]: { 
        icon: '●', 
        gradient: ['rgba(34, 197, 94, 0.15)', 'rgba(34, 197, 94, 0.08)'],
        borderColor: 'rgba(74, 222, 128, 0.4)',
        textColor: '#BBF7D0',
        iconColor: '#BBF7D0'
      },
      [AgentStatus.AWAITING_INPUT]: { 
        icon: '●', 
        gradient: ['rgba(234, 179, 8, 0.15)', 'rgba(234, 179, 8, 0.08)'],
        borderColor: 'rgba(250, 204, 21, 0.4)',
        textColor: '#FEF08A',
        iconColor: '#FEF08A'
      },
      [AgentStatus.COMPLETED]: { 
        icon: '✓', 
        gradient: ['rgba(107, 114, 128, 0.15)', 'rgba(107, 114, 128, 0.08)'],
        borderColor: 'rgba(156, 163, 175, 0.4)',
        textColor: '#E5E7EB',
        iconColor: '#E5E7EB'
      },
      [AgentStatus.FAILED]: { 
        icon: '✕', 
        gradient: ['rgba(239, 68, 68, 0.15)', 'rgba(239, 68, 68, 0.08)'],
        borderColor: 'rgba(248, 113, 113, 0.4)',
        textColor: '#FECACA',
        iconColor: '#FECACA'
      },
      [AgentStatus.KILLED]: { 
        icon: '●', 
        gradient: ['rgba(107, 114, 128, 0.15)', 'rgba(107, 114, 128, 0.08)'],
        borderColor: 'rgba(156, 163, 175, 0.4)',
        textColor: '#E5E7EB',
        iconColor: '#E5E7EB'
      },
      [AgentStatus.PAUSED]: { 
        icon: '⏸', 
        gradient: [withAlpha(theme.colors.info, 0.15), withAlpha(theme.colors.info, 0.08)],
        borderColor: withAlpha(theme.colors.info, 0.4),
        textColor: theme.colors.infoLight,
        iconColor: theme.colors.infoLight
      },
      [AgentStatus.STALE]: { 
        icon: '●', 
        gradient: ['rgba(249, 115, 22, 0.15)', 'rgba(249, 115, 22, 0.08)'],
        borderColor: 'rgba(251, 146, 60, 0.4)',
        textColor: '#FED7AA',
        iconColor: '#FED7AA'
      },
    };

    const config = statusConfig[status] || { 
      icon: '?', 
      gradient: ['rgba(107, 114, 128, 0.15)', 'rgba(107, 114, 128, 0.08)'],
      borderColor: 'rgba(156, 163, 175, 0.4)',
      textColor: '#E5E7EB',
      iconColor: '#E5E7EB'
    };
    
    return (
      <View key={status} style={[styles.statusPill, { backgroundColor: config.gradient[0], borderColor: config.borderColor }]}>
          <Text style={[styles.statusIcon, { color: config.iconColor }]}>{config.icon}</Text>
          <Text style={[styles.statusCount, { color: config.textColor }]}>{count}</Text>
          <Text style={[styles.statusLabel, { color: config.textColor }]}>
            {status.replace('_', ' ').toLowerCase()}
          </Text>
      </View>
    );
  };

  return (
    <View>
      {/* Header with Manage Agents button */}
      {agentsToShow.length > 0 && (
        <View style={styles.header}>
          <View style={styles.titleContainer}>
            <Bot size={20} color={theme.colors.white} />
            <Text style={styles.title}>Agent Fleet Status</Text>
          </View>
          <TouchableOpacity
            onPress={() => (navigation as any).navigate('UserAgents')}
            style={styles.manageButton}
            activeOpacity={0.7}
          >
            <Settings size={16} color={theme.colors.textMuted} />
            <Text style={styles.manageButtonText}>Manage Agents</Text>
          </TouchableOpacity>
        </View>
      )}
      
      {agentsToShow.map((agent) => {
        const hasWebhook = !!agent.webhook_type;
        const totalInstances = agent.instance_count || 0;
        const activeInstances = agent.active_instance_count || 0;
        const waitingInstances = agent.waiting_instance_count || 0;
        const completedInstances = agent.completed_instance_count || 0;
        const errorInstances = agent.error_instance_count || 0;
        
        return (
        <TouchableOpacity
          key={agent.id}
          onPress={() => handleAgentClick(agent)}
          activeOpacity={0.8}
        >
          <View style={styles.card}>
              <View style={styles.content}>
                <View style={styles.leftSection}>
                  <View style={styles.iconContainer}>
                    {getAgentIcon(agent.name)}
                  </View>
                  <View style={styles.infoSection}>
                    <View style={styles.headerRow}>
                      <View style={styles.nameContainer}>
                        <Text style={styles.agentName}>{formatAgentTypeName(agent.name)}</Text>
                        {hasWebhook && (
                          <Text style={styles.webhookIndicator}>🔗</Text>
                        )}
                      </View>
                      <View style={styles.rightSection}>
                        <Text style={styles.instanceCount}>{totalInstances} instance{totalInstances !== 1 ? 's' : ''}</Text>
                        {hasWebhook && (
                          <TouchableOpacity
                            onPress={(e) => handleLaunchAgent(agent, e)}
                            style={styles.launchButton}
                            activeOpacity={0.7}
                          >
                            <Plus size={16} color={theme.colors.white} strokeWidth={2.5} />
                          </TouchableOpacity>
                        )}
                      </View>
                    </View>
                    <View style={styles.statusContainer}>
                      {activeInstances > 0 && getStatusPill(AgentStatus.ACTIVE, activeInstances)}
                      {waitingInstances > 0 && getStatusPill(AgentStatus.AWAITING_INPUT, waitingInstances)}
                      {errorInstances > 0 && getStatusPill(AgentStatus.FAILED, errorInstances)}
                    </View>
                  </View>
                </View>
              </View>
          </View>
        </TouchableOpacity>
        );
      })}
      
      <LaunchAgentModal
        visible={isModalVisible}
        onClose={() => {
          setIsModalVisible(false);
          setSelectedAgent(null);
        }}
        agent={selectedAgent}
        onLaunchSuccess={handleLaunchSuccess}
      />
    </View>
  );
};

const styles = StyleSheet.create({
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.md,
  },
  titleContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.spacing.sm,
  },
  title: {
    fontSize: theme.fontSize.xl,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold,
    color: theme.colors.white,
  },
  manageButton: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.spacing.xs / 2,
    paddingHorizontal: theme.spacing.sm,
    paddingVertical: theme.spacing.xs,
    backgroundColor: 'rgba(255, 255, 255, 0.1)',
    borderRadius: theme.borderRadius.md,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.2)',
  },
  manageButtonText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium,
    color: theme.colors.textMuted,
  },
  loadingContainer: {
    paddingVertical: theme.spacing.xl,
    alignItems: 'center',
  },
  loadingText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
  card: {
    marginHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.lg,
    backgroundColor: theme.colors.authContainer,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.1)',
    padding: theme.spacing.md,
  },
  content: {
    flexDirection: 'row',
    alignItems: 'flex-start',
  },
  leftSection: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    flex: 1,
    gap: theme.spacing.md,
  },
  iconContainer: {
    width: 64,
    height: 64,
    borderRadius: theme.borderRadius.full,
    backgroundColor: withAlpha(theme.colors.primary, 0.15),
    justifyContent: 'center',
    alignItems: 'center',
  },
  infoSection: {
    flex: 1,
    paddingTop: theme.spacing.xs,
  },
  headerRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: theme.spacing.sm,
  },
  nameContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    flex: 1,
  },
  agentName: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold,
    color: theme.colors.white,
  },
  webhookIndicator: {
    fontSize: theme.fontSize.sm,
    marginLeft: theme.spacing.xs,
  },
  rightSection: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  instanceCount: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.7)',
  },
  launchButton: {
    marginLeft: theme.spacing.sm,
    backgroundColor: theme.colors.primary,
    borderRadius: theme.borderRadius.full,
    width: 32,
    height: 32,
    justifyContent: 'center',
    alignItems: 'center',
  },
  statusContainer: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 8,
    alignItems: 'flex-start',
  },
  statusPill: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: theme.spacing.sm,
    paddingVertical: theme.spacing.xs,
    borderRadius: theme.borderRadius.full,
    borderWidth: 1,
    minHeight: 28,
  },
  statusIcon: {
    fontSize: 10,
    fontWeight: 'bold',
    marginRight: theme.spacing.xs / 2,
  },
  statusCount: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold,
    marginRight: theme.spacing.xs / 2,
  },
  statusLabel: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium,
    textTransform: 'capitalize',
  },
});
