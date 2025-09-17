import React, { useCallback } from 'react';
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
import { StatusBar as DashboardStatusBar } from '@/components/dashboard/StatusBar';
import { AgentsList } from '@/components/dashboard/AgentsList';
import { RecentActivity } from '@/components/dashboard/RecentActivity';

export const MainScreen: React.FC = () => {
  console.log('[MainScreen] Component rendered');
  
  const navigation = useNavigation();
  const queryClient = useQueryClient();
  const [refreshing, setRefreshing] = React.useState(false);
  const [isScreenFocused, setIsScreenFocused] = React.useState(true);

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

  // Calculate metrics
  const metrics = React.useMemo(() => {
    if (!agentTypes) return { total: 0, active: 0, awaiting: 0, hasQuestions: false };

    let total = 0;
    let active = 0;
    let awaiting = 0;
    let hasQuestions = false;

    agentTypes.forEach(type => {
      type.recent_instances.forEach(instance => {
        total++;
        if (instance.status === AgentStatus.ACTIVE) active++;
        if (instance.status === AgentStatus.AWAITING_INPUT) {
          awaiting++;
          if (instance.pending_questions_count && instance.pending_questions_count > 0) {
            hasQuestions = true;
          }
        }
      });
    });

    return { total, active, awaiting, hasQuestions };
  }, [agentTypes]);

  const onRefresh = React.useCallback(async () => {
    setRefreshing(true);
    await queryClient.invalidateQueries({ queryKey: ['agent-types'] });
    setRefreshing(false);
  }, [queryClient]);

  const openDrawer = () => {
    (navigation as any).openDrawer();
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
              <User size={20} color={theme.colors.white} strokeWidth={1.5} />
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

        <ScrollView
          style={styles.scrollView}
          contentContainerStyle={styles.scrollContent}
          showsVerticalScrollIndicator={false}
          refreshControl={
            <RefreshControl
              refreshing={refreshing}
              onRefresh={onRefresh}
              tintColor={theme.colors.primaryLight}
            />
          }
        >
          {/* Dashboard Status Bar - Shows agent status */}
          <DashboardStatusBar agentTypes={agentTypes || []} />

          {/* Agents List */}
          <AgentsList />

          {/* Recent Activity */}
          <RecentActivity agentTypes={agentTypes || []} />
        </ScrollView>
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
    color: 'rgba(255, 255, 255, 0.6)',
  },
  scrollView: {
    flex: 1,
  },
  scrollContent: {
    paddingTop: theme.spacing.sm, // Very small padding from header
    paddingBottom: theme.spacing.xl * 2,
  },
});