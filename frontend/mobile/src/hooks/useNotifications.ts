import { useEffect, useState, useCallback } from 'react';
import { useNavigation } from '@react-navigation/native';
import * as Notifications from 'expo-notifications';
import Constants from 'expo-constants';
import { notificationService } from '@/services/notifications';
import { useAuth } from '@/contexts/AuthContext';
import { reportError } from '@/lib/logger';

interface NotificationHookReturn {
  permissionStatus: Notifications.PermissionStatus | null;
  isInitialized: boolean;
  isFullyInitialized: boolean;
  hasError: boolean;
  error: string | null;
  isInitializing: boolean;
  requestPermissions: () => Promise<boolean>;
  openSettings: () => Promise<void>;
  retryInitialization: () => Promise<boolean>;
  retryTokenRegistration: () => Promise<boolean>;
  getDetailedState: () => any;
}

// Check if we're running in Expo Go
const isExpoGo = Constants.executionEnvironment === 'storeClient';

export const useNotifications = (): NotificationHookReturn => {
  const navigation = useNavigation();
  const { session } = useAuth();
  const [permissionStatus, setPermissionStatus] = useState<Notifications.PermissionStatus | null>(null);
  const [isInitializing, setIsInitializing] = useState(false);
  const [initializationAttempts, setInitializationAttempts] = useState(0);
  const [lastInitializationTime, setLastInitializationTime] = useState<number | null>(null);

  // Get current state from notification service
  const notificationState = notificationService.getState();
  const isInitialized = notificationState.isInitialized;
  const isFullyInitialized = notificationService.isFullyInitialized();
  const hasError = notificationService.hasError();
  const error = notificationService.getError();

  // Initialize notification services
  useEffect(() => {
    let notificationListener: Notifications.Subscription;
    let responseListener: Notifications.Subscription;

    const initializeServices = async () => {
      console.log('useNotifications effect running, userId:', session?.user?.id);
      
      // Only initialize if we have a session and not already initializing
      if (!session || isInitializing) {
        console.log('Skipping initialization - no session or already initializing');
        return;
      }

      setIsInitializing(true);
      setInitializationAttempts(prev => prev + 1);

      // Initialize notification service in the background - don't await
      console.log('Starting notification service initialization in background...');
      
      notificationService.initialize(session?.user?.id).then(result => {
        if (result.success) {
          console.log('Notification service initialized successfully');
        } else if (result.partialSuccess) {
          console.warn('Notification service partially initialized:', result.error);
        } else {
          reportError(result.error ?? 'Notification service initialization failed', {
            context: 'Notification service initialization failed',
            tags: { feature: 'mobile-notifications' },
          });
        }

        // Check permission status after initialization
        return notificationService.getPermissionStatus();
      }).then(status => {
        setPermissionStatus(status);
        setLastInitializationTime(Date.now());
        
        // If we have a token but it's not registered, try to register it
        const currentState = notificationService.getState();
        if (currentState.tokenGenerated && !currentState.tokenRegistered) {
          console.log('Token generated but not registered, attempting registration...');
          return notificationService.retryTokenRegistration();
        }
      }).catch(error => {
        reportError(error, {
          context: 'Critical error during notification service initialization',
          tags: { feature: 'mobile-notifications' },
        });
      }).finally(() => {
        setIsInitializing(false);
      });

      // Set up notification listeners immediately (these are lightweight)
      try {
        const listeners = notificationService.setupNotificationListeners();
        notificationListener = listeners.notificationListener;
        responseListener = listeners.responseListener;

        // Override the response listener to handle navigation
        responseListener.remove();
        responseListener = Notifications.addNotificationResponseReceivedListener(
          (response) => {
            console.log('Notification response received:', response);
            handleNotificationResponse(response);
          }
        );
      } catch (error) {
        reportError(error, {
          context: 'Error setting up notification listeners',
          tags: { feature: 'mobile-notifications' },
        });
      }
    };

    initializeServices();

    return () => {
      if (notificationListener) {
        notificationListener.remove();
      }
      if (responseListener) {
        responseListener.remove();
      }
    };
  }, [session?.user?.id]);

  // Auto-retry initialization if it failed and we haven't tried too many times
  useEffect(() => {
    if (hasError && 
        initializationAttempts < 3 && 
        !isInitializing &&
        lastInitializationTime &&
        Date.now() - lastInitializationTime > 30000) { // Wait 30 seconds between retries
      
      console.log(`Auto-retrying notification initialization (attempt ${initializationAttempts + 1}/3)`);
      
      // Retry initialization
      const timer = setTimeout(() => {
        retryInitialization().catch(error =>
          reportError(error, {
            context: 'Automatic notification initialization retry failed',
            tags: { feature: 'mobile-notifications' },
          })
        );
      }, 2000 * initializationAttempts); // Exponential backoff: 2s, 4s, 6s

      return () => clearTimeout(timer);
    }
  }, [hasError, initializationAttempts, isInitializing, lastInitializationTime]);

  const handleNotificationResponse = useCallback((response: Notifications.NotificationResponse) => {
    console.log('[useNotifications] Notification response received:', JSON.stringify(response, null, 2));
    const data = response.notification.request.content.data;
    console.log('[useNotifications] Notification data:', JSON.stringify(data, null, 2));
    
    if (data && data.type === 'new_question') {
      const { instanceId, questionId } = data;
      console.log('[useNotifications] Navigating to instance detail - instanceId:', instanceId, 'questionId:', questionId);
      
      // Navigate to the instance detail screen
      try {
        (navigation as any).navigate('InstanceDetail', { 
          instanceId: instanceId,
          highlightQuestion: questionId 
        });
        console.log('[useNotifications] Navigation successful');
      } catch (error) {
        reportError(error, {
          context: '[useNotifications] Failed to navigate to instance detail',
          tags: { feature: 'mobile-notifications' },
        });
      }
    } else {
      console.log('[useNotifications] Notification data does not match expected format for navigation');
    }
  }, [navigation]);

  const requestPermissions = useCallback(async (): Promise<boolean> => {
    try {
      const { status } = await Notifications.requestPermissionsAsync();
      setPermissionStatus(status);
      
      if (status === 'granted') {
        // Retry initialization after getting permissions
        await retryInitialization();
        return true;
      }
      
      return false;
    } catch (error) {
      reportError(error, {
        context: 'Failed to request permissions',
        tags: { feature: 'mobile-notifications' },
      });
      return false;
    }
  }, []);

  const retryInitialization = useCallback(async (): Promise<boolean> => {
    if (isInitializing) return false;

    setIsInitializing(true);
    setInitializationAttempts(prev => prev + 1);

    try {
      console.log('Manually retrying notification initialization...');
      const result = await notificationService.retryInitialization(session?.user?.id);
      
      if (result.success) {
        console.log('Notification initialization retry successful');
        // Update permission status
        const status = await notificationService.getPermissionStatus();
        setPermissionStatus(status);
        setLastInitializationTime(Date.now());
        return true;
      } else {
        console.warn('Notification initialization retry failed:', result.error);
        return false;
      }
    } catch (error) {
      reportError(error, {
        context: 'Failed to retry notification initialization',
        tags: { feature: 'mobile-notifications' },
      });
      return false;
    } finally {
      setIsInitializing(false);
    }
  }, [isInitializing]);

  const retryTokenRegistration = useCallback(async (): Promise<boolean> => {
    try {
      console.log('Manually retrying token registration...');
      const success = await notificationService.retryTokenRegistration();
      
      if (success) {
        console.log('Token registration retry successful');
      } else {
        console.warn('Token registration retry failed');
      }
      
      return success;
    } catch (error) {
      reportError(error, {
        context: 'Failed to retry token registration',
        tags: { feature: 'mobile-notifications' },
      });
      return false;
    }
  }, []);

  const openSettings = useCallback(async () => {
    try {
      await notificationService.openSettings();
    } catch (error) {
      reportError(error, {
        context: 'Failed to open settings',
        tags: { feature: 'mobile-notifications' },
      });
    }
  }, []);

  const getDetailedState = useCallback(() => {
    return {
      ...notificationService.getState(),
      isInitializing,
      initializationAttempts,
      lastInitializationTime,
      permissionStatus,
    };
  }, [isInitializing, initializationAttempts, lastInitializationTime, permissionStatus]);

  return {
    permissionStatus,
    isInitialized,
    isFullyInitialized,
    hasError,
    error,
    isInitializing,
    requestPermissions,
    openSettings,
    retryInitialization,
    retryTokenRegistration,
    getDetailedState,
  };
};
