import { useEffect } from 'react';
import { useSearchParams, useNavigate } from 'react-router-dom';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Progress } from '@/components/ui/progress';
import { useSubscription, useUsage } from '@/hooks/useSubscription';
import { billingApi } from '@/lib/api/billing';
import { PLANS } from '@/lib/stripe';
import { useToast } from '@/hooks/use-toast';
import { format } from 'date-fns';
import { AlertCircle, Users, Calendar, TrendingUp } from 'lucide-react';
import { Alert, AlertDescription } from '@/components/ui/alert';

export default function BillingPage() {
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();
  const { data: subscription, isLoading: subLoading, refetch: refetchSub } = useSubscription();
  const { data: usage, isLoading: usageLoading, refetch: refetchUsage } = useUsage();
  const { toast } = useToast();

  // Handle successful checkout return
  useEffect(() => {
    const sessionId = searchParams.get('session_id');
    if (sessionId) {
      toast({
        title: 'Payment successful!',
        description: 'Your subscription has been updated.',
      });
      // Refetch subscription data
      refetchSub();
      refetchUsage();
      // Remove session_id from URL
      navigate('/dashboard/billing', { replace: true });
    }
  }, [searchParams, toast, navigate, refetchSub, refetchUsage]);

  const handleCancelSubscription = async () => {
    if (!window.confirm('Are you sure you want to cancel your subscription?')) {
      return;
    }

    try {
      await billingApi.cancelSubscription();
      toast({
        title: 'Subscription cancelled',
        description: 'Your subscription will remain active until the end of the billing period.',
      });
      refetchSub();
    } catch (error) {
      toast({
        title: 'Error',
        description: 'Failed to cancel subscription. Please try again.',
        variant: 'destructive'
      });
    }
  };

  const handleUpgrade = () => {
    navigate('/pricing');
  };

  if (subLoading || usageLoading) {
    return <div className="text-white">Loading billing information...</div>;
  }

  const currentPlan = subscription ? PLANS[subscription.plan_type as keyof typeof PLANS] || PLANS.free : PLANS.free;
  const usagePercentage = usage && subscription ? 
    (subscription.agent_limit === -1 ? 0 : (usage.total_agents / subscription.agent_limit) * 100) : 0;

  return (
    <div className="space-y-6 animate-fade-in">
      <div>
        <h2 className="text-3xl font-bold tracking-tight text-white">Billing & Usage</h2>
        <p className="text-lg text-off-white/80 mt-2">
          Manage your subscription and monitor usage
        </p>
      </div>

      {/* Current Plan */}
      <Card className="border border-white/20 bg-white/10 backdrop-blur-md">
        <CardHeader>
          <CardTitle className="text-white">Current Plan</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="space-y-4">
            <div>
              <h3 className="text-2xl font-bold text-white">{currentPlan.name}</h3>
              <p className="text-off-white/70">
                {currentPlan.priceDisplay}/month
              </p>
            </div>

            {subscription?.cancel_at_period_end && (
              <Alert className="bg-red-500/20 border-red-400/30">
                <AlertCircle className="h-4 w-4 !text-white" />
                <AlertDescription className="text-red-200">
                  Your subscription will be cancelled at the end of the current billing period
                </AlertDescription>
              </Alert>
            )}

            <div className="flex items-center justify-between">
              <div className="flex gap-3">
              {subscription?.plan_type !== 'enterprise' && (
                <Button 
                  onClick={handleUpgrade}
                  className="bg-white text-midnight-blue hover:bg-off-white transition-all duration-300 shadow-lg hover:shadow-xl border-0"
                >
                  Upgrade Plan
                </Button>
              )}
              {subscription?.plan_type !== 'free' && !subscription?.cancel_at_period_end && (
                <Button 
                  variant="outline" 
                  onClick={handleCancelSubscription}
                  className="border-white/20 text-off-white bg-white/10 hover:bg-white/20 hover:text-white transition-all duration-300"
                >
                  Cancel Subscription
                </Button>
              )}
              </div>
              {subscription?.current_period_end && (
                <div className="flex items-center gap-2 text-sm text-off-white/60">
                  <Calendar className="h-4 w-4" />
                  Next billing: {format(new Date(subscription.current_period_end), 'MMM d, yyyy')}
                </div>
              )}
            </div>
          </div>
        </CardContent>
      </Card>

      {/* Usage Statistics */}
      <div className="grid gap-6">
        <Card className="border border-white/20 bg-white/10 backdrop-blur-md">
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-white">
              <Users className="h-5 w-5 text-electric-accent" />
              Monthly Agents
            </CardTitle>
            <CardDescription className="text-off-white/70">
              Total agents created this month
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <div>
                <div className="flex items-center justify-between mb-2">
                  <span className="text-2xl font-bold text-white">
                    {usage?.total_agents || 0}
                  </span>
                  <span className="text-off-white/70">
                    {subscription?.agent_limit === -1 ? 'Unlimited' : `of ${subscription?.agent_limit || 0} per month`}
                  </span>
                </div>
                {subscription?.agent_limit !== -1 && (
                  <Progress value={usagePercentage} className="h-2 bg-white/10" />
                )}
              </div>
              
              {usagePercentage >= 80 && subscription?.agent_limit !== -1 && (
                <Alert className="bg-orange-500/20 border-orange-400/30">
                  <TrendingUp className="h-4 w-4 text-orange-400" />
                  <AlertDescription className="text-orange-200">
                    You're approaching your monthly agent limit. Consider upgrading for unlimited agents.
                  </AlertDescription>
                </Alert>
              )}
            </div>
          </CardContent>
        </Card>

      </div>

      {/* Plan Features */}
      <Card className="border border-white/20 bg-white/10 backdrop-blur-md">
        <CardHeader>
          <CardTitle className="text-white">Plan Features</CardTitle>
          <CardDescription className="text-off-white/70">
            Everything included in your {currentPlan.name} plan
          </CardDescription>
        </CardHeader>
        <CardContent>
          <ul className="space-y-2">
            {currentPlan.features.map((feature, index) => (
              <li key={index} className="flex items-center gap-2">
                <div className="h-2 w-2 bg-electric-accent rounded-full" />
                <span className="text-sm text-off-white/80">{feature}</span>
              </li>
            ))}
          </ul>
        </CardContent>
      </Card>
    </div>
  );
}