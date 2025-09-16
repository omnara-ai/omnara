export interface Subscription {
  isActive: boolean;
  productId: string | null;
  expirationDate: Date | null;
  willRenew: boolean;
  periodType: 'normal' | 'intro' | 'trial' | null;
  managementUrl?: string;
  provider?: 'apple' | 'google' | 'stripe' | null;
}


export const PRODUCT_ID = 'com.omnara.app.Monthly';