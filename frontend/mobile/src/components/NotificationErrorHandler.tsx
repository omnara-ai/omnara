import React, { useState } from 'react';
import { View, Text, TouchableOpacity, StyleSheet, ActivityIndicator, Alert } from 'react-native';
import { useNotifications } from '@/hooks/useNotifications';
import { theme } from '@/constants/theme';
import { withAlpha } from '@/lib/color';

interface NotificationErrorHandlerProps {
  showRetry?: boolean;
  showDetails?: boolean;
  style?: any;
  children?: React.ReactNode;
  autoHide?: boolean;
}

export const NotificationErrorHandler: React.FC<NotificationErrorHandlerProps> = ({
  showRetry = true,
  showDetails = false,
  style,
  children,
  autoHide = false,
}) => {
  const { 
    hasError, 
    error, 
    isInitializing, 
    isFullyInitialized,
    retryInitialization, 
    retryTokenRegistration,
    getDetailedState,
    permissionStatus 
  } = useNotifications();

  const [isRetrying, setIsRetrying] = useState(false);
  const [isHidden, setIsHidden] = useState(false);

  // Don't show if no error, fully initialized, or hidden
  if (!hasError || isFullyInitialized || (autoHide && isHidden)) {
    return <>{children}</>;
  }

  const handleRetry = async () => {
    setIsRetrying(true);
    
    try {
      // First try to retry initialization
      const initSuccess = await retryInitialization();
      
      if (initSuccess) {
        // If initialization succeeded, try token registration
        try {
          await retryTokenRegistration();
        } catch (tokenError) {
          console.warn('Token registration failed, but initialization succeeded:', tokenError);
        }
      }
    } catch (error) {
      console.error('Retry failed:', error);
    } finally {
      setIsRetrying(false);
    }
  };

  const handleShowDetails = () => {
    const state = getDetailedState();
    Alert.alert(
      'Notification Status Details',
      `Permissions: ${permissionStatus || 'Unknown'}\n` +
      `Initialized: ${state.isInitialized ? 'Yes' : 'No'}\n` +
      `Permissions Granted: ${state.permissionsGranted ? 'Yes' : 'No'}\n` +
      `Token Generated: ${state.tokenGenerated ? 'Yes' : 'No'}\n` +
      `Token Registered: ${state.tokenRegistered ? 'Yes' : 'No'}\n` +
      `Attempts: ${state.initializationAttempts}\n` +
      `Error: ${error || 'None'}`,
      [{ text: 'OK' }]
    );
  };

  const handleHide = () => {
    setIsHidden(true);
  };

  const getErrorMessage = () => {
    if (error?.includes('timeout')) {
      return 'Notification setup is taking longer than expected. This may be due to network issues or server delays.';
    }
    if (error?.includes('permission')) {
      return 'Notification permissions are required to receive updates. Please enable them in your device settings.';
    }
    if (error?.includes('token')) {
      return 'Unable to register for push notifications. You may not receive notifications until this is resolved.';
    }
    if (error?.includes('network') || error?.includes('connect')) {
      return 'Unable to connect to the notification server. Please check your internet connection.';
    }
    return error || 'Notification setup encountered an issue';
  };

  const getErrorSeverity = () => {
    if (permissionStatus === 'denied') return 'high';
    if (error?.includes('token')) return 'medium';
    return 'low';
  };

  const severity = getErrorSeverity();
  const isLoading = isInitializing || isRetrying;

  return (
    <View style={[styles.container, style]}>
      <View style={[
        styles.errorContainer,
        severity === 'high' && styles.errorHigh,
        severity === 'medium' && styles.errorMedium,
        severity === 'low' && styles.errorLow,
      ]}>
        <View style={styles.errorHeader}>
          <Text style={[
            styles.errorTitle,
            severity === 'high' && styles.errorTitleHigh,
            severity === 'medium' && styles.errorTitleMedium,
            severity === 'low' && styles.errorTitleLow,
          ]}>
            {severity === 'high' ? '⚠️ Notifications Disabled' : 
             severity === 'medium' ? '⚠️ Notification Issue' : 
             '⚠️ Notification Warning'}
          </Text>
          
          {autoHide && (
            <TouchableOpacity onPress={handleHide} style={styles.hideButton}>
              <Text style={styles.hideButtonText}>×</Text>
            </TouchableOpacity>
          )}
        </View>
        
        <Text style={[
          styles.errorMessage,
          severity === 'high' && styles.errorMessageHigh,
          severity === 'medium' && styles.errorMessageMedium,
          severity === 'low' && styles.errorMessageLow,
        ]}>
          {getErrorMessage()}
        </Text>
        
        <View style={styles.buttonContainer}>
          {showRetry && (
            <TouchableOpacity 
              style={[
                styles.retryButton,
                severity === 'high' && styles.retryButtonHigh,
                severity === 'medium' && styles.retryButtonMedium,
                severity === 'low' && styles.retryButtonLow,
              ]} 
              onPress={handleRetry}
              disabled={isLoading}
            >
              {isLoading ? (
                <ActivityIndicator size="small" color="#fff" />
              ) : (
                <Text style={styles.retryButtonText}>
                  {severity === 'high' ? 'Enable Notifications' : 'Retry Setup'}
                </Text>
              )}
            </TouchableOpacity>
          )}
          
          {showDetails && (
            <TouchableOpacity 
              style={styles.detailsButton} 
              onPress={handleShowDetails}
            >
              <Text style={styles.detailsButtonText}>Details</Text>
            </TouchableOpacity>
          )}
        </View>
      </View>
      
      {children}
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  errorContainer: {
    borderRadius: 8,
    padding: 16,
    margin: 16,
    borderWidth: 1,
  },
  errorHigh: {
    backgroundColor: '#FEF2F2',
    borderColor: '#FCA5A5',
  },
  errorMedium: {
    backgroundColor: '#FFF7ED',
    borderColor: '#FDBA74',
  },
  errorLow: {
    backgroundColor: withAlpha(theme.colors.info, 0.12),
    borderColor: withAlpha(theme.colors.info, 0.4),
  },
  errorHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 8,
  },
  errorTitle: {
    fontSize: 16,
    fontWeight: 'bold',
    flex: 1,
  },
  errorTitleHigh: {
    color: '#991B1B',
  },
  errorTitleMedium: {
    color: '#C2410C',
  },
  errorTitleLow: {
    color: theme.colors.infoDark,
  },
  hideButton: {
    padding: 4,
    marginLeft: 8,
  },
  hideButtonText: {
    fontSize: 20,
    color: '#6B7280',
    fontWeight: 'bold',
  },
  errorMessage: {
    fontSize: 14,
    marginBottom: 16,
    lineHeight: 20,
  },
  errorMessageHigh: {
    color: '#7F1D1D',
  },
  errorMessageMedium: {
    color: '#9A3412',
  },
  errorMessageLow: {
    color: theme.colors.infoDark,
  },
  buttonContainer: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
  },
  retryButton: {
    paddingHorizontal: 16,
    paddingVertical: 8,
    borderRadius: 6,
    minWidth: 120,
    alignItems: 'center',
    flex: 1,
    marginRight: 8,
  },
  retryButtonHigh: {
    backgroundColor: '#EF4444',
  },
  retryButtonMedium: {
    backgroundColor: '#F97316',
  },
  retryButtonLow: {
    backgroundColor: theme.colors.info,
  },
  retryButtonText: {
    color: '#fff',
    fontSize: 14,
    fontWeight: '600',
  },
  detailsButton: {
    paddingHorizontal: 16,
    paddingVertical: 8,
    borderRadius: 6,
    backgroundColor: '#F3F4F6',
    borderWidth: 1,
    borderColor: '#D1D5DB',
  },
  detailsButtonText: {
    color: '#374151',
    fontSize: 14,
    fontWeight: '500',
  },
});

export default NotificationErrorHandler; 
