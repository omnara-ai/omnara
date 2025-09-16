import React, { useState, useMemo } from 'react';
import {
  View,
  Text,
  StyleSheet,
  ScrollView,
  TouchableOpacity,
  ActivityIndicator,
  RefreshControl,
  FlatList,
} from 'react-native';
import { SafeAreaView, useSafeAreaInsets } from 'react-native-safe-area-context';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigation, useRoute } from '@react-navigation/native';
import { theme } from '@/constants/theme';
import { dashboardApi } from '@/services/api';
import { AgentStatus, AgentType, AgentInstance } from '@/types';
import { SwipeableInstanceCard } from '@/components/instances/SwipeableInstanceCard';
import { Header } from '@/components/ui';
import { formatAgentTypeName } from '@/utils/formatters';

type FilterType = 'all' | 'active' | 'waiting' | 'completed';

const FILTER_OPTIONS: Array<{ key: FilterType; label: string }> = [
  { key: 'all', label: 'All' },
  { key: 'active', label: 'Active' },
  { key: 'waiting', label: 'Waiting' },
  { key: 'completed', label: 'Completed' },
];

export const AllInstancesScreen: React.FC = () => {
  const navigation = useNavigation();
  const route = useRoute();
  const queryClient = useQueryClient();
  const insets = useSafeAreaInsets();
  const [refreshing, setRefreshing] = useState(false);
  const [activeFilter, setActiveFilter] = useState<FilterType>('all');
  
  // Get filterByAgentId and agentName from route params if they exist
  const filterByAgentId = (route.params as any)?.filterByAgentId;
  const agentName = (route.params as any)?.agentName;

  console.log('[AllInstancesScreen] Component rendered - filterByAgentId:', filterByAgentId, 'agentName:', agentName);

  // Fetch all agent types to get all instances
  const { data: agentTypes, isLoading } = useQuery({
    queryKey: ['agent-types'],
    queryFn: () => {
      console.log('[AllInstancesScreen] Fetching agent types');
      return dashboardApi.getAgentTypes();
    },
  });

  console.log('[AllInstancesScreen] Query state - isLoading:', isLoading, 'agentTypes count:', agentTypes?.length || 0);

  // Flatten all instances from all agent types
  const allInstances = useMemo(() => {
    console.log('[AllInstancesScreen] Computing allInstances - agentTypes:', agentTypes?.length || 0);
    if (!agentTypes) return [];
    
    const instances: Array<AgentInstance & { agentTypeName: string; agentTypeId: string }> = [];
    
    agentTypes.forEach(type => {
      console.log('[AllInstancesScreen] Processing agent type:', type.name, 'instances:', type.recent_instances.length);
      
      // If we have a filter, only include instances from that agent type
      if (filterByAgentId && type.id !== filterByAgentId) {
        return;
      }
      
      type.recent_instances.forEach(instance => {
        instances.push({
          ...instance,
          agentTypeName: type.name,
          agentTypeId: type.id,
        });
      });
    });

    console.log('[AllInstancesScreen] Total instances found:', instances.length, 'filtered by agent:', filterByAgentId);

    // Sort by most recent activity
    return instances.sort((a, b) => {
      const aTime = new Date(a.latest_message_at || a.started_at).getTime();
      const bTime = new Date(b.latest_message_at || b.started_at).getTime();
      return bTime - aTime;
    });
  }, [agentTypes, filterByAgentId]);

  // Filter instances based on active filter
  const filteredInstances = useMemo(() => {
    if (activeFilter === 'all') return allInstances;
    
    return allInstances.filter(instance => {
      switch (activeFilter) {
        case 'active':
          return instance.status === AgentStatus.ACTIVE;
        case 'waiting':
          return instance.status === AgentStatus.AWAITING_INPUT;
        case 'completed':
          return [
            AgentStatus.COMPLETED,
            AgentStatus.FAILED,
            AgentStatus.KILLED
          ].includes(instance.status);
        default:
          return true;
      }
    });
  }, [allInstances, activeFilter]);

  const onRefresh = async () => {
    console.log('[AllInstancesScreen] Refreshing data');
    setRefreshing(true);
    await queryClient.invalidateQueries({ queryKey: ['agent-types'] });
    setRefreshing(false);
    console.log('[AllInstancesScreen] Refresh completed');
  };

  const handleInstancePress = (instanceId: string) => {
    console.log('[AllInstancesScreen] Navigating to instance detail:', instanceId);
    (navigation as any).navigate('InstanceDetail', { instanceId });
  };

  const renderFilterPill = ({ key, label }: typeof FILTER_OPTIONS[0]) => {
    const isActive = activeFilter === key;
    
    return (
      <TouchableOpacity
        key={key}
        style={[styles.filterPill, isActive && styles.filterPillActive]}
        onPress={() => setActiveFilter(key)}
        activeOpacity={0.7}
      >
        <Text style={[styles.filterPillText, isActive && styles.filterPillTextActive]}>
          {label}
        </Text>
      </TouchableOpacity>
    );
  };

  if (isLoading) {
    console.log('[AllInstancesScreen] Showing loading state');
    return (
      <View style={styles.loadingContainer}>
        <ActivityIndicator size="large" color={theme.colors.primary} />
      </View>
    );
  }

  console.log('[AllInstancesScreen] Rendering main content - filtered instances:', filteredInstances.length);

  return (
    <View style={styles.container}>
      <SafeAreaView style={styles.safeArea}>
        {/* Header */}
        <Header 
          title={agentName ? formatAgentTypeName(agentName) : "All Instances"} 
          onBack={() => navigation.goBack()} 
        />
        
        <View style={styles.filterSection}>
          <ScrollView
            horizontal
            showsHorizontalScrollIndicator={false}
            style={styles.filterContainer}
            contentContainerStyle={styles.filterContent}
          >
            {FILTER_OPTIONS.map(renderFilterPill)}
          </ScrollView>
        </View>

        <FlatList
          data={filteredInstances}
          keyExtractor={(item) => item.id}
          renderItem={({ item }) => (
            <SwipeableInstanceCard
              instance={item}
              agentTypeName={item.agentTypeName}
              onPress={() => handleInstancePress(item.id)}
            />
          )}
          contentContainerStyle={styles.listContent}
          showsVerticalScrollIndicator={false}
          refreshControl={
            <RefreshControl
              refreshing={refreshing}
              onRefresh={onRefresh}
              tintColor={theme.colors.primaryLight}
            />
          }
          ListEmptyComponent={
            <View style={styles.emptyContainer}>
              <Text style={styles.emptyText}>
                No {activeFilter !== 'all' ? activeFilter : ''} instances found
              </Text>
            </View>
          }
        />
      </SafeAreaView>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: theme.colors.background,
  },
  safeArea: {
    flex: 1,
  },
  loadingContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    backgroundColor: theme.colors.background,
  },
  filterSection: {
    paddingTop: theme.spacing.md,
    paddingHorizontal: theme.spacing.lg,
    paddingBottom: theme.spacing.md,
  },
  filterContainer: {
    marginHorizontal: -theme.spacing.lg,
  },
  filterContent: {
    paddingHorizontal: theme.spacing.lg,
  },
  filterPill: {
    paddingHorizontal: theme.spacing.md,
    paddingVertical: theme.spacing.sm,
    borderRadius: theme.borderRadius.full,
    backgroundColor: 'rgba(255, 255, 255, 0.1)',
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.2)',
    marginRight: theme.spacing.sm,
  },
  filterPillActive: {
    backgroundColor: theme.colors.primary,
    borderColor: theme.colors.primary,
  },
  filterPillText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium,
    color: 'rgba(255, 255, 255, 0.7)',
  },
  filterPillTextActive: {
    color: theme.colors.white,
  },
  listContent: {
    paddingTop: theme.spacing.sm,
    paddingBottom: theme.spacing.xl * 3,
  },
  emptyContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    paddingVertical: theme.spacing.xl * 3,
  },
  emptyText: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.5)',
  },
});