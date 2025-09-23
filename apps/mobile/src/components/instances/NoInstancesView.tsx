import React from 'react';
import {
  View,
  Text,
  StyleSheet,
} from 'react-native';
import { theme } from '@/constants/theme';
import { OnboardingStepsSimple } from '@/components/onboarding/OnboardingStepsSimple';

export const NoInstancesView: React.FC = () => {
  return (
    <View style={styles.container}>
      <View style={styles.emptyState}>
        <Text style={styles.title}>Welcome to Omnara!</Text>
        <Text style={styles.subtitle}>
          Get started by setting up Claude Code to connect with your agents
        </Text>
      </View>

      <View style={styles.onboardingContainer}>
        <OnboardingStepsSimple />
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    paddingHorizontal: theme.spacing.lg,
    paddingVertical: theme.spacing.xl,
    justifyContent: 'center',
    alignItems: 'center',
  },
  emptyState: {
    alignItems: 'center',
    marginBottom: theme.spacing.xl,
  },
  title: {
    fontSize: theme.fontSize.xl,
    fontFamily: theme.fontFamily.bold,
    fontWeight: theme.fontWeight.bold as any,
    color: theme.colors.white,
    textAlign: 'center',
    marginBottom: theme.spacing.sm,
  },
  subtitle: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
    textAlign: 'center',
    lineHeight: theme.fontSize.base * 1.4,
  },
  onboardingContainer: {
    width: '100%',
  },
});