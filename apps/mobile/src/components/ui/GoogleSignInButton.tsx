import React from 'react';
import { TouchableOpacity, Text, View, StyleSheet, ActivityIndicator, Image } from 'react-native';
import { useAuth } from '@/contexts/AuthContext';
import { Alert } from 'react-native';
import { theme } from '@/constants/theme';
import { Ionicons } from '@expo/vector-icons';
import { reportError } from '@/lib/logger';

interface GoogleSignInButtonProps {
  disabled?: boolean;
  onSuccess?: () => void;
  onError?: (error: Error) => void;
}

export const GoogleSignInButton: React.FC<GoogleSignInButtonProps> = ({
  disabled = false,
  onSuccess,
  onError,
}) => {
  const { signInWithGoogleNative } = useAuth();
  const [loading, setLoading] = React.useState(false);

  const handleGoogleSignIn = async () => {
    if (loading || disabled) return;
    
    setLoading(true);
    try {
      await signInWithGoogleNative();
      onSuccess?.();
    } catch (error: any) {
      reportError(error, {
        context: 'Google Sign-In error',
        tags: { feature: 'mobile-auth' },
      });
      onError?.(error);
      Alert.alert('Sign-In Error', error.message || 'An error occurred during sign-in');
    } finally {
      setLoading(false);
    }
  };

  return (
    <TouchableOpacity
      style={[styles.button, disabled && styles.disabled]}
      onPress={handleGoogleSignIn}
      disabled={disabled || loading}
      activeOpacity={0.8}
    >
      <View style={styles.content}>
        {loading ? (
          <ActivityIndicator size="small" color="#FFFFFF" />
        ) : (
          <>
            <Image 
              source={require('../../../assets/google-logo.png')}
              style={styles.googleLogo}
              resizeMode="contain"
            />
            <Text style={styles.text}>Continue with Google</Text>
          </>
        )}
      </View>
    </TouchableOpacity>
  );
};

const styles = StyleSheet.create({
  button: {
    backgroundColor: 'rgba(255, 255, 255, 0.1)',
    borderRadius: theme.borderRadius.md,
    paddingVertical: theme.spacing.sm + 2, // Match Button lg size
    paddingHorizontal: theme.spacing.lg,
    minHeight: 44, // Match Button lg size
    width: '100%',
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.2)',
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
  },
  disabled: {
    opacity: 0.5,
  },
  content: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
  },
  googleLogo: {
    width: 18,
    height: 18,
    marginRight: 8,
  },
  text: {
    color: theme.colors.white,
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
  },
});

export default GoogleSignInButton;
