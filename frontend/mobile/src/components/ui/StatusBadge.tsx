import React from 'react';
import { View, Text, StyleSheet } from 'react-native';
import { LinearGradient } from 'expo-linear-gradient';
import { theme } from '@/constants/theme';
import { withAlpha } from '@/lib/color';
import { AgentStatus } from '@/types';

interface StatusBadgeProps {
  status: AgentStatus;
}

export const StatusBadge: React.FC<StatusBadgeProps> = ({ status }) => {
  const getStatusConfig = (status: AgentStatus) => {
    switch (status) {
      case AgentStatus.ACTIVE:
        return {
          label: 'Active',
          gradient: ['rgba(34, 197, 94, 0.15)', 'rgba(34, 197, 94, 0.08)'] as const,
          borderColor: 'rgba(74, 222, 128, 0.4)',
          textColor: '#BBF7D0',
        };
      case AgentStatus.AWAITING_INPUT:
        return {
          label: 'Waiting',
          gradient: ['rgba(234, 179, 8, 0.15)', 'rgba(234, 179, 8, 0.08)'] as const,
          borderColor: 'rgba(250, 204, 21, 0.4)',
          textColor: '#FEF08A',
        };
      case AgentStatus.PAUSED:
        return {
          label: 'Paused',
          gradient: [withAlpha(theme.colors.info, 0.15), withAlpha(theme.colors.info, 0.08)] as const,
          borderColor: withAlpha(theme.colors.info, 0.4),
          textColor: theme.colors.infoLight,
        };
      case AgentStatus.STALE:
        return {
          label: 'Stale',
          gradient: ['rgba(249, 115, 22, 0.15)', 'rgba(249, 115, 22, 0.08)'] as const,
          borderColor: 'rgba(251, 146, 60, 0.4)',
          textColor: '#FED7AA',
        };
      case AgentStatus.COMPLETED:
        return {
          label: 'Completed',
          gradient: ['rgba(107, 114, 128, 0.15)', 'rgba(107, 114, 128, 0.08)'] as const,
          borderColor: 'rgba(156, 163, 175, 0.4)',
          textColor: '#E5E7EB',
        };
      case AgentStatus.FAILED:
        return {
          label: 'Failed',
          gradient: ['rgba(239, 68, 68, 0.15)', 'rgba(239, 68, 68, 0.08)'] as const,
          borderColor: 'rgba(248, 113, 113, 0.4)',
          textColor: '#FECACA',
        };
      case AgentStatus.KILLED:
        return {
          label: 'Killed',
          gradient: ['rgba(239, 68, 68, 0.15)', 'rgba(239, 68, 68, 0.08)'] as const,
          borderColor: 'rgba(248, 113, 113, 0.4)',
          textColor: '#FECACA',
        };
      default:
        return {
          label: 'Unknown',
          gradient: ['rgba(107, 114, 128, 0.15)', 'rgba(107, 114, 128, 0.08)'] as const,
          borderColor: 'rgba(156, 163, 175, 0.4)',
          textColor: '#E5E7EB',
        };
    }
  };

  const config = getStatusConfig(status);

  return (
    <View style={[styles.badgeContainer, { borderColor: config.borderColor }]}>
      <LinearGradient
        colors={config.gradient}
        start={{ x: 0, y: 0 }}
        end={{ x: 0, y: 1 }}
        style={styles.badge}
      >
        <Text style={[styles.text, { color: config.textColor }]}>
          {config.label}
        </Text>
      </LinearGradient>
    </View>
  );
};

const styles = StyleSheet.create({
  badgeContainer: {
    borderRadius: theme.borderRadius.full,
    borderWidth: 1,
    overflow: 'hidden',
    ...theme.shadow.sm,
  },
  badge: {
    paddingHorizontal: theme.spacing.sm,
    paddingVertical: theme.spacing.xs,
  },
  text: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold,
    textTransform: 'capitalize',
  },
});
