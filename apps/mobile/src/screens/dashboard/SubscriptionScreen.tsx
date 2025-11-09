import React, { useState } from 'react';
import {
  View,
  Text,
  StyleSheet,
  ScrollView,
  Alert,
  TouchableOpacity,
  Linking,
  ActivityIndicator,
  RefreshControl,
  Platform,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useNavigation } from '@react-navigation/native';
import { Check, RefreshCw, ExternalLink } from 'lucide-react-native';
import { LinearGradient } from 'expo-linear-gradient';
import { theme } from '@/constants/theme';
import { Header } from '@/components/ui';
import { SubscriptionCard, PurchaseButton } from '@/components/subscription';
import { useSubscription } from '@/hooks/useSubscription';
import { PRO_FEATURES } from '@/types/subscription';
import { subscriptionService } from '@/services/subscriptionService';

export const SubscriptionScreen: React.FC = () => {
  const navigation = useNavigation();
  const {
    subscription,
    isLoading,
    purchase,
    restore,
    isPurchasing,
    isRestoring,
    isProUser,
    refetch,
  } = useSubscription();
  const [isSyncing, setIsSyncing] = useState(false);

  const handlePurchase = async () => {
    try {
      await purchase();
      
      // Show processing state
      setIsSyncing(true);
      Alert.alert(
        'Processing...',
        'Your purchase is being processed. This may take a few seconds.',
        [{ text: 'OK' }]
      );
      
      // Wait for webhook to sync
      const synced = await subscriptionService.waitForSubscriptionSync();
      setIsSyncing(false);
      
      if (synced) {
        refetch(); // Refresh UI
        Alert.alert(
          'Success!',
          'Welcome to Omnara Pro! You now have unlimited access.',
          [{ text: 'OK' }]
        );
      } else {
        Alert.alert(
          'Purchase Complete',
          'Your purchase was successful. It may take a moment to activate. Please refresh in a few seconds.',
          [{ text: 'OK' }]
        );
      }
    } catch (error: any) {
      setIsSyncing(false);
      if (error.message !== 'Purchase cancelled') {
        Alert.alert(
          'Purchase Failed',
          error.message || 'Something went wrong. Please try again.',
          [{ text: 'OK' }]
        );
      }
    }
  };

  const handleRestore = async () => {
    try {
      const customerInfo = await restore();
      const hasActiveSubscription = customerInfo.entitlements.active['Pro'] || 
                                   customerInfo.activeSubscriptions.length > 0;
      
      if (hasActiveSubscription) {
        // Show processing state
        setIsSyncing(true);
        Alert.alert(
          'Processing...',
          'Restoring your subscription. This may take a few seconds.',
          [{ text: 'OK' }]
        );
        
        // Wait for webhook to sync
        const synced = await subscriptionService.waitForSubscriptionSync();
        setIsSyncing(false);
        
        if (synced) {
          refetch(); // Refresh UI
          Alert.alert(
            'Restored!',
            'Your Pro subscription has been restored successfully.',
            [{ text: 'OK' }]
          );
        } else {
          Alert.alert(
            'Restore Complete',
            'Your subscription was found. It may take a moment to activate. Please refresh in a few seconds.',
            [{ text: 'OK' }]
          );
        }
      } else {
        Alert.alert(
          'No Subscription Found',
          'No active subscription found for this Apple ID.',
          [{ text: 'OK' }]
        );
      }
    } catch (error: any) {
      setIsSyncing(false);
      Alert.alert(
        'Restore Failed',
        error.message || 'Failed to restore purchases. Please try again.',
        [{ text: 'OK' }]
      );
    }
  };

  const handleManageSubscription = () => {
    // Handle based on provider
    if (subscription.provider === 'stripe') {
      // Open Omnara website for Stripe subscriptions
      Linking.openURL('https://claude.omnara.com');
    } else if (subscription.provider === 'apple' && Platform.OS === 'ios') {
      // Use management URL for Apple subscriptions on iOS
      if (subscription.managementUrl) {
        Linking.openURL(subscription.managementUrl);
      } else {
        Linking.openURL('https://apps.apple.com/account/subscriptions');
      }
    } else if (subscription.provider === 'google' && Platform.OS === 'android') {
      // Open Play Store subscriptions for Google
      Linking.openURL('https://play.google.com/store/account/subscriptions');
    }
    // For cross-platform subscriptions, do nothing as button should be hidden
  };
  
  // Determine if manage button should be shown
  const shouldShowManageButton = isProUser && (
    subscription.provider === 'stripe' || 
    (Platform.OS === 'ios' && subscription.provider === 'apple') ||
    (Platform.OS === 'android' && subscription.provider === 'google')
  );

  return (
    <View style={styles.container}>
      <SafeAreaView style={styles.safeArea}>
        <Header 
          title="Subscription"
          onBack={() => navigation.goBack()}
        />

        <ScrollView
          style={styles.scrollView}
          contentContainerStyle={styles.scrollContent}
          showsVerticalScrollIndicator={false}
          refreshControl={
            <RefreshControl
              refreshing={false}
              onRefresh={refetch}
              tintColor={theme.colors.primary}
            />
          }
        >
          {/* Subscription Plans */}
          <SubscriptionCard subscription={subscription} />

          {/* Action Buttons */}
          <View style={styles.actionContainer}>
            {!isProUser ? (
              <>
                <PurchaseButton
                  onPress={handlePurchase}
                  isLoading={isPurchasing || isSyncing}
                  disabled={isLoading || isSyncing}
                />

                <TouchableOpacity
                  style={styles.restoreButton}
                  onPress={handleRestore}
                  disabled={isRestoring || isLoading || isSyncing}
                  activeOpacity={0.7}
                >
                  {isRestoring ? (
                    <RefreshCw 
                      size={20} 
                      color={theme.colors.primaryLight} 
                      strokeWidth={2}
                      style={styles.spinning}
                    />
                  ) : (
                    <RefreshCw 
                      size={20} 
                      color={theme.colors.primaryLight} 
                      strokeWidth={2}
                    />
                  )}
                  <Text style={styles.restoreText}>Restore Purchases</Text>
                </TouchableOpacity>
              </>
            ) : shouldShowManageButton ? (
              <TouchableOpacity
                style={styles.manageButton}
                onPress={handleManageSubscription}
                activeOpacity={0.7}
              >
                <ExternalLink 
                  size={20} 
                  color={theme.colors.primaryLight} 
                  strokeWidth={2}
                />
                <Text style={styles.manageText}>
                  {subscription.provider === 'stripe' ? 'Manage on Web' : 'Manage Subscription'}
                </Text>
              </TouchableOpacity>
            ) : null}
          </View>

          {/* Syncing indicator */}
          {isSyncing && (
            <View style={styles.syncingContainer}>
              <ActivityIndicator size="small" color={theme.colors.pro} />
              <Text style={styles.syncingText}>Syncing subscription status...</Text>
            </View>
          )}

          {/* Terms and Legal Links */}
          <View style={styles.termsContainer}>
            {Platform.OS === 'ios' && (
              <Text style={styles.termsText}>
                Subscriptions automatically renew unless canceled at least 24 hours before the end of the current period. 
                Your Apple ID account will be charged for renewal within 24 hours before the end of the current period.
              </Text>
            )}
            
            <View style={styles.legalLinksContainer}>
              <Text style={styles.legalText}>
                By subscribing, you agree to our{' '}
                <Text 
                  style={styles.legalLink}
                  onPress={() => Linking.openURL('https://claude.omnara.com/terms')}
                >
                  Terms of Use
                </Text>
                {' '}and{' '}
                <Text 
                  style={styles.legalLink}
                  onPress={() => Linking.openURL('https://claude.omnara.com/privacy')}
                >
                  Privacy Policy
                </Text>
              </Text>
            </View>
          </View>
        </ScrollView>
      </SafeAreaView>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: theme.colors.background,
  },
  safeArea: {
    flex: 1,
  },
  scrollView: {
    flex: 1,
  },
  scrollContent: {
    paddingTop: theme.spacing.lg,
    paddingBottom: theme.spacing.xl * 3,
  },
  featuresContainer: {
    marginHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.xl,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: 'rgba(212, 173, 252, 0.3)',
    overflow: 'hidden',
  },
  featuresCard: {
    padding: theme.spacing.lg,
    borderRadius: theme.borderRadius.lg,
  },
  featuresTitle: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.lg,
  },
  featureRow: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: theme.spacing.md,
  },
  featureText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.8)',
    marginLeft: theme.spacing.md,
    flex: 1,
  },
  priceInfo: {
    marginTop: theme.spacing.lg,
    paddingTop: theme.spacing.lg,
    borderTopWidth: 1,
    borderTopColor: 'rgba(255, 255, 255, 0.1)',
    alignItems: 'center',
  },
  priceText: {
    fontSize: theme.fontSize['2xl'],
    fontFamily: theme.fontFamily.bold,
    fontWeight: theme.fontWeight.bold as any,
    color: theme.colors.pro,
    marginBottom: theme.spacing.xs,
  },
  priceSubtext: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.6)',
  },
  actionContainer: {
    marginHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.xl,
  },
  restoreButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    paddingVertical: theme.spacing.md,
    marginTop: theme.spacing.md,
  },
  restoreText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.primaryLight,
    marginLeft: theme.spacing.sm,
  },
  manageButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    paddingVertical: theme.spacing.md,
    paddingHorizontal: theme.spacing.lg,
    borderRadius: theme.borderRadius.md,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.2)',
  },
  manageText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.primaryLight,
    marginLeft: theme.spacing.sm,
  },
  termsContainer: {
    marginHorizontal: theme.spacing.lg,
    paddingHorizontal: theme.spacing.md,
  },
  termsText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.4)',
    textAlign: 'center',
    lineHeight: 18,
    marginBottom: theme.spacing.md,
  },
  legalLinksContainer: {
    marginTop: theme.spacing.sm,
  },
  legalText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.5)',
    textAlign: 'center',
    lineHeight: 20,
  },
  legalLink: {
    color: theme.colors.primaryLight,
    textDecorationLine: 'underline',
  },
  spinning: {
    // Add rotation animation if needed
  },
  syncingContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    marginHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.md,
    padding: theme.spacing.md,
    backgroundColor: 'rgba(212, 173, 252, 0.1)',
    borderRadius: theme.borderRadius.md,
  },
  syncingText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.pro,
    marginLeft: theme.spacing.sm,
  },
});