import React, { useCallback, useState, useMemo } from 'react';
import {
  View,
  Text,
  StyleSheet,
  ScrollView,
  RefreshControl,
  TouchableOpacity,
  ActivityIndicator,
  Platform,
  StatusBar,
  FlatList,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { theme } from '@/constants/theme';
import { dashboardApi } from '@/services/api';
import { AgentType, AgentInstance, AgentStatus } from '@/types';
import { useNavigation, useFocusEffect } from '@react-navigation/native';
import { User } from 'lucide-react-native';
import { Header } from '@/components/ui';

// Components
import { SwipeableInstanceCard } from '@/components/instances/SwipeableInstanceCard';
import { NoInstancesView } from '@/components/instances/NoInstancesView';

type FilterType = 'all' | 'active' | 'waiting' | 'completed';

const FILTER_OPTIONS: Array<{ key: FilterType; label: string }> = [
  { key: 'all', label: 'All' },
  { key: 'active', label: 'Active' },
  { key: 'waiting', label: 'Waiting' },
  { key: 'completed', label: 'Completed' },
];

export const MainScreen: React.FC = () => {
  console.log('[MainScreen] Component rendered');
  
  const navigation = useNavigation();
  const queryClient = useQueryClient();
  const [refreshing, setRefreshing] = React.useState(false);
  const [isScreenFocused, setIsScreenFocused] = React.useState(true);
  const [activeFilter, setActiveFilter] = useState<FilterType>('all');

  // Fetch agent types and instances
  const { data: agentTypes, isLoading } = useQuery({
    queryKey: ['agent-types'],
    queryFn: () => dashboardApi.getAgentTypes(),
    refetchInterval: isScreenFocused ? 5000 : false,
  });

  // Refresh on focus
  useFocusEffect(
    useCallback(() => {
      setIsScreenFocused(true);
      queryClient.invalidateQueries({ queryKey: ['agent-types'] });
      
      return () => {
        setIsScreenFocused(false);
      };
    }, [queryClient])
  );

  // Flatten all instances from all agent types
  const allInstances = useMemo(() => {
    if (!agentTypes) return [];

    const instances: Array<AgentInstance & { agentTypeName: string; agentTypeId: string }> = [];

    agentTypes.forEach(type => {
      type.recent_instances.forEach(instance => {
        instances.push({
          ...instance,
          agentTypeName: type.name,
          agentTypeId: type.id,
        });
      });
    });

    // Sort by most recent activity
    return instances.sort((a, b) => {
      const aTime = new Date(a.latest_message_at || a.started_at).getTime();
      const bTime = new Date(b.latest_message_at || b.started_at).getTime();
      return bTime - aTime;
    });
  }, [agentTypes]);

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

  // Calculate metrics
  const metrics = React.useMemo(() => {
    if (!allInstances.length) return { total: 0, active: 0, awaiting: 0, hasQuestions: false };

    let total = 0;
    let active = 0;
    let awaiting = 0;
    let hasQuestions = false;

    allInstances.forEach(instance => {
      total++;
      if (instance.status === AgentStatus.ACTIVE) active++;
      if (instance.status === AgentStatus.AWAITING_INPUT) {
        awaiting++;
        if (instance.pending_questions_count && instance.pending_questions_count > 0) {
          hasQuestions = true;
        }
      }
    });

    return { total, active, awaiting, hasQuestions };
  }, [allInstances]);

  const onRefresh = React.useCallback(async () => {
    setRefreshing(true);
    await queryClient.invalidateQueries({ queryKey: ['agent-types'] });
    setRefreshing(false);
  }, [queryClient]);

  const openDrawer = () => {
    (navigation as any).openDrawer();
  };

  const handleInstancePress = (instanceId: string) => {
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
    return (
      <View style={styles.loadingContainer}>
        <ActivityIndicator size="large" color={theme.colors.primary} />
      </View>
    );
  }

  return (
    <View style={styles.container}>
      <StatusBar backgroundColor={theme.colors.background} barStyle="light-content" />
      <SafeAreaView style={styles.safeArea}>
        {/* Header */}
        <Header 
          title="Omnara"
          leftContent={
            <TouchableOpacity 
              style={styles.profileButton}
              onPress={openDrawer}
              activeOpacity={0.7}
            >
              <User size={20} color={theme.colors.text} strokeWidth={1.5} />
            </TouchableOpacity>
          }
          rightContent={
            metrics.active > 0 && (
              <View style={styles.activeIndicator}>
                <View style={styles.activeDot} />
                <Text style={styles.activeText}>{metrics.active}</Text>
              </View>
            )
          }
        />

        {/* Filter Section - only show if there are instances */}
        {allInstances.length > 0 && (
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
        )}

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
            allInstances.length === 0 ? (
              <NoInstancesView />
            ) : (
              <View style={styles.emptyContainer}>
                <Text style={styles.emptyText}>
                  No {activeFilter !== 'all' ? activeFilter : ''} instances found
                </Text>
              </View>
            )
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
  profileButton: {
    width: 32,
    height: 32,
    justifyContent: 'center',
    alignItems: 'center',
  },
  activeIndicator: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
  },
  activeDot: {
    width: 6,
    height: 6,
    borderRadius: theme.borderRadius.full,
    backgroundColor: theme.colors.success,
  },
  activeText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.textMuted,
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
    backgroundColor: theme.colors.cardSurface,
    borderWidth: 1,
    borderColor: theme.colors.border,
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
    color: theme.colors.textMuted,
  },
  filterPillTextActive: {
    color: theme.colors.black,
  },
  listContent: {
    paddingTop: theme.spacing.sm,
    paddingBottom: theme.spacing.xl * 2,
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