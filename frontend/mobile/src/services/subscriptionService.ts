import { Platform } from 'react-native';
import Purchases, {
  PurchasesOffering,
  CustomerInfo,
} from 'react-native-purchases';
import { Subscription, PRODUCT_ID } from '@/types/subscription';
import { dashboardApi } from '@/services/api';

class SubscriptionService {
  private initialized = false;

  async initialize(apiKey: string, userId: string): Promise<void> {
    if (this.initialized || Platform.OS !== 'ios') {
      return;
    }

    if (!userId) {
      throw new Error('User ID is required for RevenueCat initialization');
    }

    try {
      Purchases.configure({ apiKey });
      
      // Log in with user ID to ensure RevenueCat uses our user ID
      const { customerInfo } = await Purchases.logIn(userId);
      
      this.initialized = true;
    } catch (error) {
      console.error('Failed to initialize RevenueCat:', error);
      throw error;
    }
  }

  async getOfferings(): Promise<PurchasesOffering | null> {
    if (Platform.OS !== 'ios') {
      return null;
    }

    try {
      const offerings = await Purchases.getOfferings();
      return offerings.current;
    } catch (error) {
      console.error('Failed to get offerings:', error);
      throw error;
    }
  }

  async purchasePro(): Promise<CustomerInfo> {
    if (Platform.OS !== 'ios') {
      throw new Error('Purchases only available on iOS');
    }

    try {
      const offerings = await this.getOfferings();
      if (!offerings) {
        throw new Error('No offerings available');
      }

      // Find the monthly package
      const monthlyPackage = offerings.availablePackages.find(
        (pkg) => pkg.product.identifier === PRODUCT_ID
      );

      if (!monthlyPackage) {
        throw new Error('Pro subscription not found');
      }

      const { customerInfo } = await Purchases.purchasePackage(monthlyPackage);
      
      // No need to sync immediately - webhook will handle it
      // Just return the customer info for UI update
      return customerInfo;
    } catch (error: any) {
      if (error.userCancelled) {
        throw new Error('Purchase cancelled');
      }
      console.error('Purchase failed:', error);
      throw error;
    }
  }

  async restorePurchases(): Promise<CustomerInfo> {
    if (Platform.OS !== 'ios') {
      throw new Error('Restore only available on iOS');
    }

    try {
      const customerInfo = await Purchases.restorePurchases();
      
      // No need to sync - webhook will handle it automatically
      return customerInfo;
    } catch (error) {
      console.error('Restore failed:', error);
      throw error;
    }
  }

  async getSubscriptionStatus(): Promise<Subscription> {
    // Default free subscription
    const defaultSubscription: Subscription = {
      isActive: false,
      productId: null,
      expirationDate: null,
      willRenew: false,
      periodType: null,
      provider: null,
    };

    try {
      // First check backend (source of truth)
      const backendStatus = await dashboardApi.getSubscriptionStatus();
      
      // If backend says pro, trust it
      if (backendStatus.plan_type === 'pro' || backendStatus.plan_type === 'enterprise') {
        const baseSubscription: Subscription = {
          isActive: true,
          productId: PRODUCT_ID, // Default to our product ID
          expirationDate: null, // Backend doesn't provide this
          willRenew: false, // Backend doesn't know this
          periodType: 'normal',
          provider: backendStatus.provider as 'apple' | 'google' | 'stripe' | null,
        };

        // If it's an Apple or Google subscription, try to get more details from RevenueCat
        if (backendStatus.provider === 'apple' || backendStatus.provider === 'google') {
          try {
            const customerInfo = await Purchases.getCustomerInfo();
            const entitlement = customerInfo.entitlements.active['Pro'];
            
            if (entitlement) {
              // Merge RevenueCat details for better UI
              return {
                ...baseSubscription,
                productId: entitlement.productIdentifier,
                expirationDate: entitlement.expirationDate ? new Date(entitlement.expirationDate) : null,
                willRenew: entitlement.willRenew,
                periodType: entitlement.periodType as 'normal' | 'intro' | 'trial',
                managementUrl: customerInfo.managementURL || undefined,
              };
            }
          } catch (error) {
            // RevenueCat details not available, use backend data only
          }
        }

        // For Stripe or if RevenueCat fetch failed, return backend data
        return baseSubscription;
      }

      // Backend says free, but double-check RevenueCat in case of race condition
      const customerInfo = await Purchases.getCustomerInfo();
      const hasProAccess = customerInfo.entitlements.active['Pro'] || 
                          customerInfo.activeSubscriptions.includes(PRODUCT_ID);

      if (hasProAccess) {
        const entitlement = customerInfo.entitlements.active['Pro'];
        return {
          isActive: true,
          productId: entitlement.productIdentifier,
          expirationDate: entitlement.expirationDate ? new Date(entitlement.expirationDate) : null,
          willRenew: entitlement.willRenew,
          periodType: entitlement.periodType as 'normal' | 'intro' | 'trial',
          managementUrl: customerInfo.managementURL || undefined,
          provider: (() => {
            // Try to determine provider from management URL
            if (customerInfo.managementURL) {
              if (customerInfo.managementURL.includes('apple.com')) {
                return 'apple';
              } else if (customerInfo.managementURL.includes('play.google')) {
                return 'google';
              }
            }
            // Fallback to platform
            return Platform.OS === 'ios' ? 'apple' : 'google';
          })(),
        };
      }

      return defaultSubscription;
    } catch (error) {
      console.error('Failed to get subscription status:', error);
      
      // If backend is down, try RevenueCat as fallback
      try {
        const customerInfo = await Purchases.getCustomerInfo();
        const hasProAccess = customerInfo.entitlements.active['Pro'] || 
                            customerInfo.activeSubscriptions.includes(PRODUCT_ID);

        if (hasProAccess) {
          const entitlement = customerInfo.entitlements.active['Pro'];
          return {
            isActive: true,
            productId: entitlement.productIdentifier,
            expirationDate: entitlement.expirationDate ? new Date(entitlement.expirationDate) : null,
            willRenew: entitlement.willRenew,
            periodType: entitlement.periodType as 'normal' | 'intro' | 'trial',
            managementUrl: customerInfo.managementURL || undefined,
            provider: (() => {
              // Try to determine provider from management URL
              if (customerInfo.managementURL) {
                if (customerInfo.managementURL.includes('apple.com')) {
                  return 'apple';
                } else if (customerInfo.managementURL.includes('play.google')) {
                  return 'google';
                }
              }
              // Fallback to platform
              return Platform.OS === 'ios' ? 'apple' : 'google';
            })(),
          };
        }
      } catch (revenueCatError) {
        console.error('RevenueCat fallback also failed:', revenueCatError);
      }

      return defaultSubscription;
    }
  }

  async isProUser(): Promise<boolean> {
    const status = await this.getSubscriptionStatus();
    return status.isActive;
  }
  
  async waitForSubscriptionSync(maxAttempts = 10, delayMs = 2000): Promise<boolean> {
    // Poll backend for subscription status after purchase
    for (let i = 0; i < maxAttempts; i++) {
      try {
        const backendStatus = await dashboardApi.getSubscriptionStatus();
        if (backendStatus.plan_type === 'pro') {
          return true;
        }
      } catch (error) {
        console.error('Failed to check backend status:', error);
      }
      
      if (i < maxAttempts - 1) {
        await new Promise(resolve => setTimeout(resolve, delayMs));
      }
    }
    return false;
  }

}

export const subscriptionService = new SubscriptionService();