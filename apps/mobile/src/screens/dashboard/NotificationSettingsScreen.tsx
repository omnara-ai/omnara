import React, { useState } from 'react';
import {
  View,
  Text,
  StyleSheet,
  ScrollView,
  TouchableOpacity,
  Alert,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useNavigation } from '@react-navigation/native';
import { LinearGradient } from 'expo-linear-gradient';
import { Gradient, Button } from '@/components/ui';
import { theme } from '@/constants/theme';
import { withAlpha } from '@/lib/color';
import { useNotifications } from '@/hooks/useNotifications';
import { dashboardApi } from '@/services/api';

export const NotificationSettingsScreen: React.FC = () => {
  const navigation = useNavigation();
  const {
    permissionStatus,
    isFullyInitialized,
    hasError,
    error,
    retryInitialization,
  } = useNotifications();

  const [isLoading, setIsLoading] = useState(false);

  const handleTestPushNotification = async () => {
    try {
      setIsLoading(true);
      
      // If notifications aren't fully set up, try to initialize them first
      if (!isFullyInitialized) {
        console.log('Notifications not fully initialized, attempting to initialize...');
        const success = await retryInitialization();
        if (!success) {
          Alert.alert(
            'Setup Required',
            'Notifications need to be set up before testing. Please enable notifications in your device settings.',
            [{ text: 'OK' }]
          );
          return;
        }
      }
      
      const result = await dashboardApi.sendTestPushNotification();
      
      if (result.success) {
        Alert.alert(
          'Real Test Sent! 🎯', 
          'A REAL push notification was sent via Expo Push API. Check your device! This uses the same system as agent questions.',
          [{ text: 'OK' }]
        );
      } else {
        Alert.alert('Test Failed', result.message || 'Failed to send real test notification');
      }
    } catch (error: any) {
      Alert.alert('Error', error.message || 'Failed to send real test notification');
    } finally {
      setIsLoading(false);
    }
  };

  const getNotificationStatus = () => {
    if (isFullyInitialized && permissionStatus === 'granted') {
      return {
        text: 'Valid',
        color: theme.colors.success,
        description: 'Notifications are fully set up and working correctly.',
      };
    } else if (permissionStatus === 'denied') {
      return {
        text: 'Denied',
        color: theme.colors.error,
        description: 'Notification permissions are denied. Please enable them in your device settings.',
      };
    } else if (hasError) {
      return {
        text: 'Error',
        color: theme.colors.error,
        description: error || 'There was an issue setting up notifications.',
      };
    } else if (permissionStatus === 'granted') {
      return {
        text: 'Partial',
        color: theme.colors.warning,
        description: 'Permissions granted but setup is incomplete. Token registration may be in progress.',
      };
    } else {
      return {
        text: 'Not Set Up',
        color: theme.colors.warning,
        description: 'Notifications are not set up. Enable them to receive alerts when your agents need input.',
      };
    }
  };

  const status = getNotificationStatus();

  return (
    <Gradient variant="dark" style={styles.container}>
      <SafeAreaView style={styles.safeArea}>
        <ScrollView
          style={styles.scrollView}
          contentContainerStyle={styles.scrollContent}
          showsVerticalScrollIndicator={false}
        >
          <View style={styles.header}>
            <TouchableOpacity 
              style={styles.backButton}
              onPress={() => navigation.goBack()}
              activeOpacity={0.7}
            >
              <Text style={styles.backArrow}>←</Text>
            </TouchableOpacity>
            <Text style={styles.title}>Notification Settings</Text>
            <Text style={styles.subtitle}>Manage your notification preferences</Text>
          </View>

          {/* Notification Status */}
          <View style={styles.statusCardContainer}>
              <LinearGradient
                colors={[withAlpha(theme.colors.primary, 0.12), withAlpha(theme.colors.primary, 0.06)]}
                start={{ x: 0, y: 0 }}
                end={{ x: 0, y: 1 }}
                style={styles.statusCard}
              >
              <View style={styles.statusHeader}>
                <Text style={styles.statusTitle}>Notification Status</Text>
                <View style={[
                  styles.statusBadge,
                  { backgroundColor: status.color + '20', borderColor: status.color + '60' }
                ]}>
                  <Text style={[styles.statusBadgeText, { color: status.color }]}>
                    {status.text}
                  </Text>
                </View>
              </View>
              <Text style={styles.statusDescription}>
                {status.description}
              </Text>
            </LinearGradient>
          </View>

          {/* Test Push Notifications */}
          {/* <View style={styles.actionCardContainer}>
              <LinearGradient
                colors={['rgba(34, 197, 94, 0.12)', 'rgba(34, 197, 94, 0.06)']}
                start={{ x: 0, y: 0 }}
                end={{ x: 0, y: 1 }}
                style={styles.actionCard}
              >
              <View style={styles.actionContent}>
                <Text style={styles.actionTitle}>🎯 Test Push Notifications</Text>
                <Text style={styles.actionDescription}>
                  Send a test notification to verify the system is working correctly
                </Text>
                <Button
                  onPress={handleTestPushNotification}
                  loading={isLoading}
                  style={styles.actionButton}
                >
                  Send Test Push
                </Button>
              </View>
            </LinearGradient>
          </View> */}
        </ScrollView>
      </SafeAreaView>
    </Gradient>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  safeArea: {
    flex: 1,
  },
  scrollView: {
    flex: 1,
  },
  scrollContent: {
    paddingBottom: theme.spacing.xl * 3,
  },
  header: {
    paddingHorizontal: theme.spacing.lg,
    paddingTop: theme.spacing.lg,
    paddingBottom: theme.spacing.xl,
  },
  backButton: {
    marginBottom: theme.spacing.md,
  },
  backArrow: {
    fontSize: theme.fontSize['2xl'],
    color: theme.colors.primaryLight,
  },
  title: {
    fontSize: theme.fontSize['3xl'],
    fontFamily: theme.fontFamily.bold,
    fontWeight: theme.fontWeight.bold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.xs,
  },
  subtitle: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.primaryLight,
  },
  statusCardContainer: {
    marginHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.xl,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: withAlpha(theme.colors.primary, 0.4),
    ...theme.shadow.lg,
    shadowColor: theme.colors.primary,
    shadowOpacity: 0.15,
    overflow: 'hidden',
  },
  statusCard: {
    borderRadius: theme.borderRadius.lg,
    padding: theme.spacing.md,
  },
  statusHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: theme.spacing.md,
  },
  statusTitle: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold,
    color: theme.colors.white,
  },
  statusDescription: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.7)',
    lineHeight: theme.fontSize.base * 1.5,
  },
  actionCardContainer: {
    marginHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.md,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: withAlpha(theme.colors.primary, 0.4),
    ...theme.shadow.lg,
    shadowColor: theme.colors.primary,
    shadowOpacity: 0.15,
    overflow: 'hidden',
  },
  actionCard: {
    borderRadius: theme.borderRadius.lg,
    padding: theme.spacing.md,
  },
  actionContent: {
    alignItems: 'flex-start',
  },
  actionTitle: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold,
    color: theme.colors.white,
    marginBottom: theme.spacing.xs,
  },
  actionDescription: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.7)',
    marginBottom: theme.spacing.md,
  },
  actionButton: {
    alignSelf: 'stretch',
    backgroundColor: theme.colors.primary,
    borderWidth: 2,
    borderColor: theme.colors.primaryLight,
    shadowColor: theme.colors.primary,
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.3,
    shadowRadius: 4,
    elevation: 4,
  },
  statusBadge: {
    paddingHorizontal: theme.spacing.sm,
    paddingVertical: theme.spacing.xs / 2,
    borderRadius: theme.borderRadius.full,
    borderWidth: 1,
  },
  statusBadgeText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium,
  },
});
