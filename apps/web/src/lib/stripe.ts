export const PLANS = {
  free: {
    id: 'free',
    name: 'Free',
    price: 0,
    priceDisplay: '$0',
    description: 'Perfect for trying out Omnara',
    features: [
      '10 agents per month',
      'Basic dashboard access',
      'Community support',
      'Standard agent features',
    ],
    limits: {
      agents: 10,
      apiCalls: -1, // unlimited
    },
    isCustom: false,
  },
  pro: {
    id: 'pro',
    name: 'Pro',
    price: 9,
    priceDisplay: '$9',
    description: 'For professionals and growing teams',
    features: [
      'Unlimited agents',
      'Priority dashboard features',
      'Email support',
      'Advanced agent analytics',
      'API access',
    ],
    limits: {
      agents: -1, // unlimited
      apiCalls: -1, // unlimited
    },
    isCustom: false,
  },
  enterprise: {
    id: 'enterprise',
    name: 'Enterprise',
    price: 0, // Custom pricing
    priceDisplay: 'Custom',
    description: 'For large organizations with custom needs',
    features: [
      'Unlimited agents',
      'Team collaboration',
      'Priority support',
      'Custom integrations',
      'Notification escalation',
      '99.9% uptime SLA',
    ],
    limits: {
      agents: -1, // unlimited
      apiCalls: -1, // unlimited
    },
    isCustom: true, // Flag to indicate custom pricing
  },
} as const;

export type PlanId = keyof typeof PLANS;