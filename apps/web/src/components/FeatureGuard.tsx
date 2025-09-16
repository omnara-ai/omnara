import { ReactNode } from 'react';
import { useNavigate } from 'react-router-dom';
import { Button } from '@/components/ui/button';
import { Alert, AlertDescription } from '@/components/ui/alert';
import { useFeatureAccess } from '@/hooks/useSubscription';
import { TrendingUp } from 'lucide-react';

interface FeatureGuardProps {
  feature: 'create_agent' | 'api_call';
  children: ReactNode;
  fallback?: ReactNode;
}

export function FeatureGuard({ feature, children, fallback }: FeatureGuardProps) {
  const navigate = useNavigate();
  const { canCreateAgent, remainingAgents, subscription, isLoading } = useFeatureAccess();

  if (isLoading) {
    return <>{children}</>;
  }

  if (feature === 'create_agent' && !canCreateAgent()) {
    return (
      <div className="space-y-4">
        {fallback || (
          <Alert>
            <TrendingUp className="h-4 w-4" />
            <AlertDescription>
              You've reached the limit of {subscription?.agent_limit} agents per month on your {subscription?.plan_type} plan.
            </AlertDescription>
          </Alert>
        )}
        <Button onClick={() => navigate('/pricing')} className="w-full">
          Upgrade Plan
        </Button>
      </div>
    );
  }

  return <>{children}</>;
}

export function UsageBadge() {
  const { usage, subscription, isLoading } = useFeatureAccess();

  if (isLoading || !usage || !subscription) {
    return null;
  }

  if (subscription.agent_limit === -1) {
    return (
      <span className="text-sm text-muted-foreground">
        {usage.total_agents} agents
      </span>
    );
  }

  const percentage = (usage.total_agents / subscription.agent_limit) * 100;
  const isNearLimit = percentage >= 80;

  return (
    <span className={`text-sm ${isNearLimit ? 'text-orange-500' : 'text-muted-foreground'}`}>
      {usage.total_agents} / {subscription.agent_limit} agents this month
    </span>
  );
}