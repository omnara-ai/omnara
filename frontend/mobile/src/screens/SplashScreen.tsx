import React from 'react';
import { View, Text, StyleSheet, ActivityIndicator } from 'react-native';
import { Gradient } from '@/components/ui';
import { theme } from '@/constants/theme';

export const SplashScreen: React.FC = () => {
  console.log('[SplashScreen] Component rendered');
  
  return (
    <Gradient variant="dark" style={styles.container}>
      <View style={styles.content}>
        <Text style={styles.title}>Omnara</Text>
        <Text style={styles.subtitle}>AI Agent Command Center</Text>
        <ActivityIndicator
          size="large"
          color={theme.colors.primaryLight}
          style={styles.loader}
        />
      </View>
    </Gradient>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  content: {
    flex: 1,
    alignItems: 'center',
    justifyContent: 'center',
    paddingHorizontal: theme.spacing.xl,
  },
  title: {
    fontSize: theme.fontSize['4xl'],
    fontWeight: theme.fontWeight.bold,
    color: theme.colors.white,
    marginBottom: theme.spacing.sm,
  },
  subtitle: {
    fontSize: theme.fontSize.lg,
    color: theme.colors.primaryLight,
    marginBottom: theme.spacing.xxl,
  },
  loader: {
    marginTop: theme.spacing.xl,
  },
});