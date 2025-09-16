import React from 'react';
import { View, Text, TouchableOpacity, StyleSheet, ActivityIndicator } from 'react-native';
import { useAuth } from '@/contexts/AuthContext';

interface AuthErrorHandlerProps {
  showRetry?: boolean;
  style?: any;
  children?: React.ReactNode;
}

export const AuthErrorHandler: React.FC<AuthErrorHandlerProps> = ({
  showRetry = true,
  style,
  children,
}) => {
  const { profileError, profileLoading, retryProfileFetch } = useAuth();

  if (!profileError) {
    return <>{children}</>;
  }

  const handleRetry = async () => {
    try {
      await retryProfileFetch();
    } catch (error) {
      console.error('Manual retry failed:', error);
    }
  };

  return (
    <View style={[styles.container, style]}>
      <View style={styles.errorContainer}>
        <Text style={styles.errorTitle}>Connection Issue</Text>
        <Text style={styles.errorMessage}>
          {profileError.includes('timeout') || profileError.includes('network') || profileError.includes('connect')
            ? 'Unable to connect to the server. Please check your internet connection and try again.'
            : profileError}
        </Text>
        
        {showRetry && (
          <TouchableOpacity 
            style={styles.retryButton} 
            onPress={handleRetry}
            disabled={profileLoading}
          >
            {profileLoading ? (
              <ActivityIndicator size="small" color="#fff" />
            ) : (
              <Text style={styles.retryButtonText}>Retry</Text>
            )}
          </TouchableOpacity>
        )}
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
    backgroundColor: '#FEF2F2',
    borderColor: '#FCA5A5',
    borderWidth: 1,
    borderRadius: 8,
    padding: 16,
    margin: 16,
    alignItems: 'center',
  },
  errorTitle: {
    fontSize: 16,
    fontWeight: 'bold',
    color: '#991B1B',
    marginBottom: 8,
  },
  errorMessage: {
    fontSize: 14,
    color: '#7F1D1D',
    textAlign: 'center',
    marginBottom: 16,
    lineHeight: 20,
  },
  retryButton: {
    backgroundColor: '#EF4444',
    paddingHorizontal: 16,
    paddingVertical: 8,
    borderRadius: 6,
    minWidth: 80,
    alignItems: 'center',
  },
  retryButtonText: {
    color: '#fff',
    fontSize: 14,
    fontWeight: '600',
  },
});

export default AuthErrorHandler; 