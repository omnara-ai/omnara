import React, { useState, useEffect } from 'react';
import { TouchableOpacity, Text, StyleSheet, Platform, View } from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import * as AppleAuthentication from 'expo-apple-authentication';
import { theme } from '@/constants/theme';

interface AppleSignInButtonProps {
  onPress: () => void;
  loading?: boolean;
}

export const AppleSignInButton: React.FC<AppleSignInButtonProps> = ({ onPress, loading }) => {
  const [isAvailable, setIsAvailable] = useState(false);

  useEffect(() => {
    // Check if Apple Sign In is available
    AppleAuthentication.isAvailableAsync().then(setIsAvailable);
  }, []);

  // Only render on iOS when Apple Sign In is available
  if (Platform.OS !== 'ios' || !isAvailable) {
    return null;
  }

  return (
    <TouchableOpacity
      style={styles.button}
      onPress={onPress}
      disabled={loading}
      activeOpacity={0.8}
    >
      <View style={styles.content}>
        <Ionicons name="logo-apple" size={20} color="#FFFFFF" style={styles.icon} />
        <Text style={styles.text}>Continue with Apple</Text>
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
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.2)',
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
  },
  content: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
  },
  icon: {
    marginRight: 8,
  },
  text: {
    color: theme.colors.white,
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
  },
});