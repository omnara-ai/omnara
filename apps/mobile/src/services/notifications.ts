import * as Notifications from 'expo-notifications';
import * as Device from 'expo-device';
import { Platform, AppState } from 'react-native';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { reportError } from '@/lib/logger';

Notifications.setNotificationHandler({
  handleNotification: async () => {
    const appState = AppState.currentState;
    const isAppInForeground = appState === 'active';
    
    return {
      shouldShowAlert: !isAppInForeground,
      shouldPlaySound: !isAppInForeground,
      shouldSetBadge: false,
      shouldShowBanner: !isAppInForeground,
      shouldShowList: !isAppInForeground,
    };
  },
});

// Retry configuration for notification operations
const RETRY_CONFIG = {
  maxRetries: 3,
  initialDelay: 1000, // 1 second
  maxDelay: 8000, // 8 seconds
  backoffFactor: 2,
  timeoutMs: 10000, // 10 seconds per operation
};

interface NotificationState {
  isInitialized: boolean;
  permissionsGranted: boolean;
  tokenGenerated: boolean;
  tokenRegistered: boolean;
  error: string | null;
}

interface InitializationResult {
  success: boolean;
  error?: string;
  partialSuccess?: boolean;
  state?: NotificationState;
}

class NotificationService {
  private static instance: NotificationService;
  private notificationToken: string | null = null;
  private lastRegisteredUserId: string | null = null;
  private listenersSetup: boolean = false;
  private state: NotificationState = {
    isInitialized: false,
    permissionsGranted: false,
    tokenGenerated: false,
    tokenRegistered: false,
    error: null,
  };

  private constructor() {}

  static getInstance(): NotificationService {
    if (!NotificationService.instance) {
      NotificationService.instance = new NotificationService();
    }
    return NotificationService.instance;
  }

  private async sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }

  private async retryOperation<T>(
    operation: () => Promise<T>,
    operationName: string,
    maxRetries: number = RETRY_CONFIG.maxRetries
  ): Promise<T> {
    for (let attempt = 0; attempt <= maxRetries; attempt++) {
      try {
        const timeoutPromise = new Promise<T>((_, reject) => {
          setTimeout(() => {
            reject(new Error(`${operationName} timed out after ${RETRY_CONFIG.timeoutMs}ms`));
          }, RETRY_CONFIG.timeoutMs);
        });

        return await Promise.race([operation(), timeoutPromise]);
      } catch (error) {
        if (attempt === maxRetries) {
          reportError(error, {
            context: `${operationName} failed after ${maxRetries + 1} attempts`,
            extras: { operationName, maxRetries: maxRetries + 1 },
            tags: { feature: 'mobile-notifications' },
          });
          throw error;
        }

        const delay = Math.min(
          RETRY_CONFIG.initialDelay * Math.pow(RETRY_CONFIG.backoffFactor, attempt),
          RETRY_CONFIG.maxDelay
        );

        console.warn(`${operationName} failed (attempt ${attempt + 1}/${maxRetries + 1}), retrying in ${delay}ms:`, error);
        await this.sleep(delay);
      }
    }
    throw new Error(`${operationName} failed after all retries`);
  }

  async initialize(userId?: string): Promise<InitializationResult> {
    // Check if we already registered for this user
    if (this.state.isInitialized && this.lastRegisteredUserId === userId && this.state.tokenRegistered) {
      console.log('Already initialized and token registered for this user');
      return { success: true, state: this.state };
    }

    // If we have a different user or token isn't registered, re-initialize
    if (this.lastRegisteredUserId !== userId) {
      console.log('Different user detected, re-initializing notifications');
      this.state = {
        isInitialized: false,
        permissionsGranted: false,
        tokenGenerated: false,
        tokenRegistered: false,
        error: null,
      };
    }

    console.log('Initializing notification service...');
    this.state.error = null;

    try {
      // Step 1: Request permissions (fastest, should complete quickly)
      console.log('Requesting notification permissions...');
      const permissionsGranted = await this.retryOperation(
        () => this.requestPermissions(),
        'Permission request',
        2 // Fewer retries for permissions
      );

      this.state.permissionsGranted = permissionsGranted;

      if (!permissionsGranted) {
        console.warn('Notification permissions not granted');
        this.state.isInitialized = true;
        return { 
          success: false, 
          error: 'Notification permissions not granted',
          state: this.state 
        };
      }

      // Step 2: Configure notification channel (Android only, fast)
      if (Platform.OS === 'android') {
        console.log('Configuring Android notification channel...');
        await this.retryOperation(
          () => this.configureAndroidChannel(),
          'Android channel configuration',
          2
        );
      }

      // Step 3: Get push token (can be slow, especially on first launch)
      console.log('Generating push token...');
      
      // Start token generation in background for faster app startup
      this.generatePushTokenInBackground();

      // Mark as initialized even without token - we'll get it in background
      this.state.tokenGenerated = false;
      this.state.tokenRegistered = false;

      this.state.isInitialized = true;
      this.lastRegisteredUserId = userId;
      
      // We consider it a partial success if we have permissions
      // Token generation happens in background
      const success = false; // Will be true only when token is fully registered
      const partialSuccess = this.state.permissionsGranted;

      console.log('Notification service initialization completed:', {
        success,
        partialSuccess,
        state: this.state,
        note: 'Token generation continuing in background'
      });

      return { success, partialSuccess, state: this.state };
    } catch (error) {
      reportError(error, {
        context: 'Critical error during notification initialization',
        tags: { feature: 'mobile-notifications' },
      });
      this.state.error = error instanceof Error ? error.message : 'Unknown error';
      this.state.isInitialized = true;
      return { 
        success: false, 
        error: this.state.error,
        state: this.state 
      };
    }
  }

  private async requestPermissions(): Promise<boolean> {
    const { status: existingStatus } = await Notifications.getPermissionsAsync();
    let finalStatus = existingStatus;

    if (existingStatus !== 'granted') {
      const { status } = await Notifications.requestPermissionsAsync();
      finalStatus = status;
    }

    return finalStatus === 'granted';
  }

  private async configureAndroidChannel(): Promise<void> {
    await Notifications.setNotificationChannelAsync('agent-questions', {
      name: 'Agent Questions',
      importance: Notifications.AndroidImportance.MAX,
      vibrationPattern: [0, 250, 250, 250],
      lightColor: '#f59e0b',
      enableVibrate: true,
      enableLights: true,
      showBadge: true,
      sound: 'default',
    });
  }

  private async generatePushToken(): Promise<boolean> {
    // Check if we're on a physical device
    if (!Device.isDevice) {
      console.warn('Push notifications only work on physical devices');
      return false;
    }

    // Get the push token  
    const tokenData = await Notifications.getExpoPushTokenAsync();
    this.notificationToken = tokenData.data;
    console.log('Push token obtained:', this.notificationToken);

    return true;
  }

  private async registerTokenWithServer(token: string): Promise<boolean> {
    // Import dashboardApi here to avoid circular dependencies
    const { dashboardApi } = await import('./api');
    
    const response = await dashboardApi.registerPushToken({
      token,
      platform: Platform.OS as 'ios' | 'android'
    });

    if (response && response.success) {
      console.log('Push token registered with server');
      // Store token locally to avoid re-registering
      await AsyncStorage.setItem('push_token', token);
      await AsyncStorage.setItem('push_token_registered', 'true');
      return true;
    } else {
      throw new Error('Server rejected push token registration');
    }
  }

  private async generatePushTokenInBackground(): Promise<void> {
    try {
      // First attempt with shorter timeout
      const tokenData = await Promise.race([
        this.generatePushToken(),
        new Promise<boolean>((_, reject) => 
          setTimeout(() => reject(new Error('Initial token generation timeout')), 3000)
        )
      ]);

      if (tokenData) {
        this.state.tokenGenerated = true;
        console.log('Push token generated successfully in background');
        
        // Now register with server
        if (this.notificationToken) {
          this.scheduleBackgroundTokenRegistration();
        }
      }
    } catch (error) {
      console.warn('Initial token generation failed, retrying in background:', error);
      
      // Retry with longer timeout in background
      setTimeout(async () => {
        try {
          const tokenGenerated = await this.retryOperation(
            () => this.generatePushToken(),
            'Background push token generation',
            2
          );
          
          if (tokenGenerated) {
            this.state.tokenGenerated = true;
            console.log('Push token generated successfully on retry');
            
            if (this.notificationToken) {
              this.scheduleBackgroundTokenRegistration();
            }
          }
        } catch (retryError) {
          reportError(retryError, {
            context: 'Failed to generate push token after retries',
            tags: { feature: 'mobile-notifications' },
          });
          this.state.error = 'Push token generation failed';
        }
      }, 5000); // Wait 5 seconds before retry
    }
  }

  private scheduleBackgroundTokenRegistration(): void {
    // Register token immediately in background
    setTimeout(async () => {
      if (this.notificationToken && !this.state.tokenRegistered) {
        console.log('Attempting background token registration...');
        try {
          const success = await this.retryOperation(
            () => this.registerTokenWithServer(this.notificationToken!),
            'Background token registration',
            2
          );
          if (success) {
            this.state.tokenRegistered = true;
            console.log('Background token registration successful');
          }
        } catch (error) {
          console.warn('Background token registration failed:', error);
          // Schedule another retry in 5 minutes
          setTimeout(() => this.scheduleBackgroundTokenRegistration(), 300000);
        }
      }
    }, 1000); // Register after just 1 second instead of 30
  }

  async retryInitialization(userId?: string): Promise<InitializationResult> {
    console.log('Retrying notification initialization...');
    // Reset state except for what we've already successfully completed
    this.state.isInitialized = false;
    this.state.error = null;
    
    return this.initialize(userId);
  }

  async retryTokenRegistration(): Promise<boolean> {
    if (!this.notificationToken) {
      // Try to generate token first if we don't have one
      console.log('No token available, attempting to generate one first...');
      try {
        const tokenGenerated = await this.generatePushToken();
        if (!tokenGenerated || !this.notificationToken) {
          throw new Error('Failed to generate push token');
        }
        this.state.tokenGenerated = true;
      } catch (error) {
        reportError(error, {
          context: 'Failed to generate token',
          tags: { feature: 'mobile-notifications' },
        });
        throw new Error('No push token available to register');
      }
    }

    console.log('Manually retrying token registration...');
    try {
      const success = await this.retryOperation(
        () => this.registerTokenWithServer(this.notificationToken!),
        'Manual token registration',
        3
      );
      
      if (success) {
        this.state.tokenRegistered = true;
        this.state.error = null;
      }
      
      return success;
    } catch (error) {
      this.state.error = error instanceof Error ? error.message : 'Token registration failed';
      throw error;
    }
  }

  // Set up notification event listeners
  setupNotificationListeners(): {
    notificationListener: Notifications.Subscription;
    responseListener: Notifications.Subscription;
  } {
    // Prevent duplicate listeners
    if (this.listenersSetup) {
      console.log('Notification listeners already set up, skipping...');
      return {
        notificationListener: { remove: () => {} } as any,
        responseListener: { remove: () => {} } as any,
      };
    }
    
    this.listenersSetup = true;
    
    const notificationListener = Notifications.addNotificationReceivedListener(
      (notification) => {
        console.log('Notification received:', notification);
        // Handle notification received while app is in foreground
      }
    );

    const responseListener = Notifications.addNotificationResponseReceivedListener(
      (response) => {
        console.log('Notification response:', response);
        // Handle user interaction with notification
        const data = response.notification.request.content.data;
        if (data && data.type === 'new_question') {
          // Navigate to the specific question/instance
          this.handleNotificationResponse(data);
        }
      }
    );

    return {
      notificationListener,
      responseListener,
    };
  }

  private handleNotificationResponse(data: any): void {
    // This will be called when user taps on a notification
    // The navigation logic should be implemented in the app component
    console.log('Handling notification response:', data);
  }

  async getPermissionStatus(): Promise<Notifications.PermissionStatus> {
    const { status } = await Notifications.getPermissionsAsync();
    return status;
  }

  async openSettings(): Promise<void> {
    // openSettingsAsync may not be available in all Expo versions
    console.warn('Opening notification settings not available in this Expo version');
  }

  async deactivatePushToken(): Promise<void> {
    try {
      const storedToken = await AsyncStorage.getItem('push_token');
      if (!storedToken) return;

      // Import dashboardApi here to avoid circular dependencies
      const { dashboardApi } = await import('./api');
      
      await dashboardApi.deactivatePushToken(storedToken);
      await AsyncStorage.removeItem('push_token');
      await AsyncStorage.removeItem('push_token_registered');
      
      console.log('Push token deactivated');
    } catch (error) {
      reportError(error, {
        context: 'Failed to deactivate push token',
        tags: { feature: 'mobile-notifications' },
      });
    }
  }

  getToken(): string | null {
    return this.notificationToken;
  }

  async isPushTokenActive(): Promise<boolean> {
    try {
      const storedToken = await AsyncStorage.getItem('push_token');
      const isRegistered = await AsyncStorage.getItem('push_token_registered');
      return !!(storedToken && isRegistered === 'true');
    } catch (error) {
      console.warn('Failed to check push token status:', error);
      return false;
    }
  }

  getState(): NotificationState {
    return { ...this.state };
  }

  isFullyInitialized(): boolean {
    return this.state.isInitialized && 
           this.state.permissionsGranted && 
           this.state.tokenGenerated && 
           this.state.tokenRegistered;
  }

  hasError(): boolean {
    return !!this.state.error;
  }

  getError(): string | null {
    return this.state.error;
  }
}

export const notificationService = NotificationService.getInstance();
