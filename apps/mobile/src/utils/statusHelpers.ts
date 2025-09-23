import { AgentStatus } from '@/types';
import { theme } from '@/constants/theme';

export const getStatusColor = (status: AgentStatus | undefined | null): string => {
  if (!status) {
    return theme.colors.textMuted;
  }
  
  switch (status) {
    case AgentStatus.ACTIVE:
      return theme.colors.success;
    case AgentStatus.AWAITING_INPUT:
      return theme.colors.warning;
    case AgentStatus.COMPLETED:
      return theme.colors.textMuted;
    case AgentStatus.FAILED:
      return theme.colors.error;
    case AgentStatus.KILLED:
      return theme.colors.textMuted;
    case AgentStatus.PAUSED:
      return theme.colors.info;
    default:
      return theme.colors.textMuted;
  }
};

export const getStatusText = (status: AgentStatus | undefined | null): string => {
  if (!status) {
    return 'Unknown';
  }
  
  switch (status) {
    case AgentStatus.ACTIVE:
      return 'Active';
    case AgentStatus.AWAITING_INPUT:
      return 'Awaiting Input';
    case AgentStatus.COMPLETED:
      return 'Completed';
    case AgentStatus.FAILED:
      return 'Failed';
    case AgentStatus.KILLED:
      return 'Terminated';
    case AgentStatus.PAUSED:
      return 'Paused';
    default:
      return 'Unknown';
  }
};