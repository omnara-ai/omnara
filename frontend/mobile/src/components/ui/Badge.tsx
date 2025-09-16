import React from 'react';
import { View, Text, StyleSheet, ViewProps } from 'react-native';
import { LinearGradient } from 'expo-linear-gradient';
import { theme } from '@/constants/theme';
import { withAlpha } from '@/lib/color';
import { AgentStatus } from '@/types';

interface BadgeProps extends ViewProps {
  variant?: 'default' | 'success' | 'warning' | 'error' | 'info' | 'secondary' | 'status';
  status?: AgentStatus;
  size?: 'sm' | 'md';
  children: React.ReactNode;
}

export const Badge: React.FC<BadgeProps> = ({
  variant = 'default',
  status,
  size = 'sm',
  style,
  children,
  ...props
}) => {
  // If status is provided, use status-specific styling
  const getStatusConfig = (status: AgentStatus) => {
    switch (status) {
      case AgentStatus.ACTIVE:
        return {
          gradient: ['rgba(34, 197, 94, 0.15)', 'rgba(34, 197, 94, 0.08)'],
          borderColor: 'rgba(74, 222, 128, 0.4)',
          textColor: '#BBF7D0',
        };
      case AgentStatus.AWAITING_INPUT:
        return {
          gradient: ['rgba(234, 179, 8, 0.15)', 'rgba(234, 179, 8, 0.08)'],
          borderColor: 'rgba(250, 204, 21, 0.4)',
          textColor: '#FEF08A',
        };
      case AgentStatus.PAUSED:
        return {
          gradient: [withAlpha(theme.colors.info, 0.15), withAlpha(theme.colors.info, 0.08)],
          borderColor: withAlpha(theme.colors.info, 0.4),
          textColor: theme.colors.infoLight,
        };
      case AgentStatus.STALE:
        return {
          gradient: ['rgba(249, 115, 22, 0.15)', 'rgba(249, 115, 22, 0.08)'],
          borderColor: 'rgba(251, 146, 60, 0.4)',
          textColor: '#FED7AA',
        };
      case AgentStatus.COMPLETED:
        return {
          gradient: ['rgba(107, 114, 128, 0.15)', 'rgba(107, 114, 128, 0.08)'],
          borderColor: 'rgba(156, 163, 175, 0.4)',
          textColor: '#E5E7EB',
        };
      case AgentStatus.FAILED:
      case AgentStatus.KILLED:
        return {
          gradient: ['rgba(239, 68, 68, 0.15)', 'rgba(239, 68, 68, 0.08)'],
          borderColor: 'rgba(248, 113, 113, 0.4)',
          textColor: '#FECACA',
        };
      default:
        return {
          gradient: ['rgba(107, 114, 128, 0.15)', 'rgba(107, 114, 128, 0.08)'],
          borderColor: 'rgba(156, 163, 175, 0.4)',
          textColor: '#E5E7EB',
        };
    }
  };

  const statusConfig = status ? getStatusConfig(status) : null;

  const variantStyles = {
    default: styles.default,
    success: styles.success,
    warning: styles.warning,
    error: styles.error,
    info: styles.info,
    secondary: styles.secondary,
    status: styles.default, // fallback for status variant
  };

  const textStyles = {
    defaultText: styles.defaultText,
    successText: styles.successText,
    warningText: styles.warningText,
    errorText: styles.errorText,
    infoText: styles.infoText,
    secondaryText: styles.secondaryText,
    statusText: styles.defaultText, // fallback for status variant
  };

  const sizeStyles = {
    sm: styles.sm,
    md: styles.md,
  };

  const sizeTextStyles = {
    smText: styles.smText,
    mdText: styles.mdText,
  };

  const badgeStyle = [
    styles.base,
    sizeStyles[size],
    style,
  ];

  const textStyle = [
    styles.text,
    sizeTextStyles[`${size}Text`],
    statusConfig ? { color: statusConfig.textColor } : textStyles[`${variant}Text`],
  ];

  if (statusConfig) {
    // Use glassmorphism for status badges
    return (
      <View style={[styles.statusContainer, { borderColor: statusConfig.borderColor }, badgeStyle]} {...props}>
        <LinearGradient
          colors={statusConfig.gradient as unknown as readonly [string, string, ...string[]]}
          start={{ x: 0, y: 0 }}
          end={{ x: 0, y: 1 }}
          style={[styles.statusBadge, sizeStyles[size]]}
        >
          <Text style={textStyle}>
            {String(children || '')}
          </Text>
        </LinearGradient>
      </View>
    );
  }

  // Use regular styling for non-status badges
  return (
    <View style={[badgeStyle, variantStyles[variant]]} {...props}>
      <Text style={textStyle}>
        {String(children || '')}
      </Text>
    </View>
  );
};

// Helper function to get badge variant from agent status
export const getStatusBadgeVariant = (status: AgentStatus): BadgeProps['variant'] => {
  switch (status) {
    case AgentStatus.ACTIVE:
      return 'success';
    case AgentStatus.AWAITING_INPUT:
      return 'warning';
    case AgentStatus.COMPLETED:
      return 'info';
    case AgentStatus.FAILED:
    case AgentStatus.KILLED:
      return 'error';
    case AgentStatus.PAUSED:
    case AgentStatus.STALE:
    default:
      return 'secondary';
  }
};

// New helper to render status badge with proper colors
export const StatusBadge: React.FC<{ status: AgentStatus; size?: 'sm' | 'md' }> = ({ 
  status, 
  size = 'sm' 
}) => {
  if (!status) {
    return null;
  }

  const statusText = String(status).replace('_', ' ').toLowerCase()
    .split(' ')
    .map(word => word.charAt(0).toUpperCase() + word.slice(1))
    .join(' ');

  return (
    <Badge status={status} size={size}>
      {statusText}
    </Badge>
  );
};

const styles = StyleSheet.create({
  base: {
    paddingHorizontal: theme.spacing.sm,
    paddingVertical: theme.spacing.xs / 2,
    borderRadius: theme.borderRadius.full,
    alignSelf: 'flex-start',
  },
  statusContainer: {
    borderRadius: theme.borderRadius.full,
    borderWidth: 1,
    overflow: 'hidden',
    alignSelf: 'flex-start',
    ...theme.shadow.sm,
  },
  statusBadge: {
    paddingHorizontal: theme.spacing.sm,
    paddingVertical: theme.spacing.xs / 2,
    borderRadius: theme.borderRadius.full,
  },
  // Sizes
  sm: {
    paddingHorizontal: theme.spacing.sm,
    paddingVertical: 2,
  },
  md: {
    paddingHorizontal: theme.spacing.md,
    paddingVertical: theme.spacing.xs,
  },
  // Variants
  default: {
    backgroundColor: theme.colors.backgroundLight,
  },
  success: {
    backgroundColor: `${theme.colors.success}20`,
  },
  warning: {
    backgroundColor: `${theme.colors.warning}20`,
  },
  error: {
    backgroundColor: `${theme.colors.error}20`,
  },
  info: {
    backgroundColor: `${theme.colors.info}20`,
  },
  secondary: {
    backgroundColor: `${theme.colors.textLight}20`,
  },
  // Text styles
  text: {
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium,
  },
  defaultText: {
    color: theme.colors.text,
  },
  successText: {
    color: theme.colors.success,
  },
  warningText: {
    color: theme.colors.warning,
  },
  errorText: {
    color: theme.colors.error,
  },
  infoText: {
    color: theme.colors.info,
  },
  secondaryText: {
    color: theme.colors.textLight,
  },
  // Text sizes
  smText: {
    fontSize: theme.fontSize.xs,
  },
  mdText: {
    fontSize: theme.fontSize.sm,
  },
});
