import { useQuery } from '@tanstack/react-query';
import { billingApi, Subscription, Usage } from '@/lib/api/billing';

export function useSubscription(options?: { enabled?: boolean }) {
  return useQuery<Subscription>({
    queryKey: ['subscription'],
    queryFn: () => billingApi.getSubscription(),
    staleTime: 5 * 60 * 1000, // 5 minutes
    retry: 1,
    enabled: options?.enabled !== false, // Default to true if not specified
  });
}

export function useUsage() {
  return useQuery<Usage>({
    queryKey: ['usage'],
    queryFn: () => billingApi.getUsage(),
    staleTime: 1 * 60 * 1000, // 1 minute
    retry: 1,
  });
}

export function useFeatureAccess() {
  const { data: subscription, isLoading } = useSubscription();
  const { data: usage } = useUsage();

  const canCreateAgent = () => {
    if (isLoading || !subscription || !usage) return true; // Allow while loading
    if (subscription.agent_limit === -1) return true; // Unlimited
    return usage.total_agents < subscription.agent_limit;
  };

  const remainingAgents = () => {
    if (!subscription || !usage) return 0;
    if (subscription.agent_limit === -1) return -1; // Unlimited
    return Math.max(0, subscription.agent_limit - usage.total_agents);
  };

  return {
    subscription,
    usage,
    canCreateAgent,
    remainingAgents,
    isLoading
  };
}