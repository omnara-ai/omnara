import React from 'react';
import { View, Text, StyleSheet } from 'react-native';
import { theme } from '@/constants/theme';
import Constants from 'expo-constants';

interface DevelopmentNoticeProps {
  feature: string;
}

export const DevelopmentNotice: React.FC<DevelopmentNoticeProps> = ({ feature }) => {
  // Only show in Expo Go
  if (Constants.appOwnership !== 'expo') {
    return null;
  }

  return (
    <View style={styles.container}>
      <Text style={styles.title}>⚠️ Expo Go Limitation</Text>
      <Text style={styles.text}>
        {feature} may not work correctly in Expo Go due to URL scheme limitations.
      </Text>
      <Text style={styles.text}>
        For full functionality, please build a development client:
      </Text>
      <Text style={styles.code}>npx expo run:ios</Text>
      <Text style={styles.or}>or</Text>
      <Text style={styles.code}>npx expo run:android</Text>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    backgroundColor: 'rgba(245, 158, 11, 0.1)',
    borderWidth: 1,
    borderColor: 'rgba(245, 158, 11, 0.3)',
    borderRadius: theme.borderRadius.md,
    padding: theme.spacing.md,
    marginVertical: theme.spacing.md,
  },
  title: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold,
    color: theme.colors.warning,
    marginBottom: theme.spacing.xs,
  },
  text: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textInverse,
    marginBottom: theme.spacing.xs,
  },
  code: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    color: theme.colors.warningLight,
    backgroundColor: 'rgba(0, 0, 0, 0.2)',
    paddingHorizontal: theme.spacing.sm,
    paddingVertical: theme.spacing.xs,
    borderRadius: theme.borderRadius.sm,
    alignSelf: 'flex-start',
    marginTop: theme.spacing.xs,
  },
  or: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
    alignSelf: 'center',
    marginVertical: theme.spacing.xs,
  },
});