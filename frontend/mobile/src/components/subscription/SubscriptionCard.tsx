import React from 'react';
import { View } from 'react-native';
import { Crown, User } from 'lucide-react-native';
import { theme } from '@/constants/theme';
import { Subscription } from '@/types/subscription';
import { format } from 'date-fns';
import { PlanCard } from './PlanCard';

interface SubscriptionCardProps {
  subscription: Subscription;
}

export const SubscriptionCard: React.FC<SubscriptionCardProps> = ({ subscription }) => {
  const isActive = subscription.isActive;

  const freeFeatures = [
    { text: '10 agents per month', included: true },
    { text: 'Basic dashboard access', included: true },
    { text: 'Community support', included: true },
  ];

  const proFeatures = [
    { text: 'Unlimited agents per month', included: true },
    { text: 'Priority support', included: true },
    { text: 'Advanced analytics', included: true },
    { text: 'API access', included: true },
    { text: 'All future features', included: true },
  ];

  const proBillingInfo = (() => {
    if (!isActive) return '$9/month';
    
    // Stripe subscriptions don't have expiration info from mobile
    if (subscription.provider === 'stripe') {
      return 'Pro Plan';
    }
    
    // Apple/Google subscriptions have renewal info
    if (subscription.expirationDate) {
      return subscription.willRenew 
        ? `Renews ${format(subscription.expirationDate, 'MMM d, yyyy')}`
        : `Expires ${format(subscription.expirationDate, 'MMM d, yyyy')}`;
    }
    
    return 'Pro Plan';
  })();

  return (
    <View>
      {/* Free Plan Card */}
      <PlanCard
        title="Free Plan"
        subtitle={!isActive ? "Current plan" : "Basic tier"}
        icon={User}
        iconColor="rgba(255, 255, 255, 0.7)"
        iconBackgroundColor={theme.colors.authContainer + '40'}
        gradientColors={[theme.colors.authContainer + '80', theme.colors.authContainer + '4D']}
        borderColor={theme.colors.authContainer + '99'}
        features={freeFeatures}
      />

      {/* Pro Plan Card */}
      <PlanCard
        title="Pro Plan"
        subtitle={isActive ? "Current plan" : "Upgrade for full access"}
        icon={Crown}
        iconColor={theme.colors.pro}
        iconBackgroundColor={theme.colors.pro + '40'}
        gradientColors={[theme.colors.pro + '26', theme.colors.pro + '14']}
        borderColor={theme.colors.pro + '4D'}
        features={proFeatures}
        additionalInfo={proBillingInfo}
      />
    </View>
  );
};