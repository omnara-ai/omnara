import React from 'react';
import {
  View,
  Text,
  StyleSheet,
  TouchableOpacity,
} from 'react-native';
import { theme } from '@/constants/theme';
import { AgentType, AgentInstance } from '@/types';
import { useNavigation } from '@react-navigation/native';
import { SwipeableRecentActivityItem } from './SwipeableRecentActivityItem';

interface RecentActivityProps {
  agentTypes: AgentType[];
}

interface ActivityItem {
  instance: AgentInstance;
  typeName: string;
  timestamp: string;
}

export const RecentActivity: React.FC<RecentActivityProps> = ({ agentTypes }) => {
  const navigation = useNavigation();

  // Collect and sort all recent activities
  const recentActivities = React.useMemo(() => {
    const activities: ActivityItem[] = [];

    agentTypes.forEach(type => {
      type.recent_instances.forEach(instance => {
        // Use latest_message_at for active instances, ended_at for completed, or started_at as fallback
        const timestamp = instance.latest_message_at || instance.ended_at || instance.started_at;
        
        activities.push({
          instance,
          typeName: type.name,
          timestamp,
        });
      });
    });

    // Sort by most recent activity first
    return activities.sort((a, b) => 
      new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime()
    ).slice(0, 5); // Show top 5 most recent
  }, [agentTypes]);

  const hasActivities = recentActivities.length > 0;

  if (!hasActivities) {
    return null;
  }

  const handlePress = (instanceId: string) => {
    (navigation as any).navigate('InstanceDetail', { instanceId });
  };

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.sectionTitle}>Recent Activity</Text>
        <TouchableOpacity
          onPress={() => navigation.navigate('AllInstances' as never)}
          activeOpacity={0.7}
        >
          <Text style={styles.viewAllLink}>View all instances</Text>
        </TouchableOpacity>
      </View>
      
      <View style={styles.card}>
        {recentActivities.map(({ instance, typeName, timestamp }, index) => (
          <SwipeableRecentActivityItem
            key={instance.id}
            instance={instance}
            typeName={typeName}
            timestamp={timestamp}
            onPress={() => handlePress(instance.id)}
            isLast={index === recentActivities.length - 1}
          />
        ))}
      </View>
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
    color: theme.colors.white,
  },
  viewAllLink: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.primaryLight,
  },
  card: {
    backgroundColor: theme.colors.authContainer,
    borderRadius: theme.borderRadius.sm,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.08)',
    overflow: 'hidden',
  },
});