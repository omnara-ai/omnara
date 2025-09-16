import React, { useState } from 'react';
import {
  View,
  Text,
  StyleSheet,
  TouchableOpacity,
  Alert,
} from 'react-native';
import { LinearGradient } from 'expo-linear-gradient';
import { Terminal } from 'lucide-react-native';
import { theme } from '@/constants/theme';
import * as Clipboard from 'expo-clipboard';
import { withAlpha } from '@/lib/color';

interface OnboardingStepsSimpleProps {}

export const OnboardingStepsSimple: React.FC<OnboardingStepsSimpleProps> = () => {
  const [copiedInstall, setCopiedInstall] = useState(false);
  const [copiedRun, setCopiedRun] = useState(false);

  const handleCopyInstall = () => {
    const command = 'pip install omnara';
    Clipboard.setStringAsync(command);
    Alert.alert('Copied!', 'Installation command copied to clipboard.');
    setCopiedInstall(true);
    setTimeout(() => setCopiedInstall(false), 2000);
  };

  const handleCopyRun = () => {
    const command = 'omnara';
    Clipboard.setStringAsync(command);
    Alert.alert('Copied!', 'Command copied to clipboard.');
    setCopiedRun(true);
    setTimeout(() => setCopiedRun(false), 2000);
  };

  return (
    <View style={styles.container}>
      <Terminal size={40} color={theme.colors.primaryLight} strokeWidth={1.5} />
      
      <Text style={styles.title}>Get Started with Claude Code</Text>
      
      <Text style={styles.description}>
        Just two simple commands to get started:
      </Text>

      {/* Step 1: Install Omnara */}
      <View style={styles.setupSection}>
        <Text style={styles.setupStepNumber}>1</Text>
        <View style={styles.setupStepContent}>
          <Text style={styles.setupStepTitle}>Install Omnara</Text>
          
          <TouchableOpacity 
            style={[
              styles.commandContainer,
              copiedInstall && styles.commandContainerCopied
            ]}
            onPress={handleCopyInstall}
            activeOpacity={0.8}
          >
            <LinearGradient
              pointerEvents="none"
              colors={[withAlpha(theme.colors.primary, 0.15), withAlpha(theme.colors.primary, 0.06), 'transparent']}
              start={{ x: 0, y: 0 }}
              end={{ x: 1, y: 1 }}
              style={[StyleSheet.absoluteFillObject, styles.commandGlow]}
            />
            <Text style={styles.commandText}>
              pip install omnara
            </Text>
            <Text style={[
              styles.copyLabel,
              copiedInstall && styles.copyLabelCopied
            ]}>
              {copiedInstall ? 'COPIED!' : 'TAP TO COPY'}
            </Text>
          </TouchableOpacity>
        </View>
      </View>

      {/* Step 2: Run Omnara */}
      <View style={styles.setupSection}>
        <Text style={styles.setupStepNumber}>2</Text>
        <View style={styles.setupStepContent}>
          <Text style={styles.setupStepTitle}>Run in your project</Text>
          
          <TouchableOpacity 
            style={[
              styles.commandContainer,
              copiedRun && styles.commandContainerCopied
            ]}
            onPress={handleCopyRun}
            activeOpacity={0.8}
          >
            <LinearGradient
              pointerEvents="none"
              colors={[withAlpha(theme.colors.primary, 0.15), withAlpha(theme.colors.primary, 0.06), 'transparent']}
              start={{ x: 0, y: 0 }}
              end={{ x: 1, y: 1 }}
              style={[StyleSheet.absoluteFillObject, styles.commandGlow]}
            />
            <Text style={styles.commandText}>
              omnara
            </Text>
            <Text style={[
              styles.copyLabel,
              copiedRun && styles.copyLabelCopied
            ]}>
              {copiedRun ? 'COPIED!' : 'TAP TO COPY'}
            </Text>
          </TouchableOpacity>
        </View>
      </View>

      {/* That's it message */}
      <Text style={styles.completionMessage}>
        ✨ That's it! You can now use Claude Code through the CLI, or connect from anywhere using the Omnara mobile app or web dashboard.
      </Text>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    alignItems: 'center',
    width: '100%',
  },
  title: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    textAlign: 'center',
    marginTop: theme.spacing.md,
    marginBottom: theme.spacing.xs,
  },
  description: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
    textAlign: 'center',
    marginBottom: theme.spacing.lg,
  },
  setupSection: {
    width: '100%',
    flexDirection: 'row',
    alignItems: 'flex-start',
    marginBottom: theme.spacing.md,
  },
  setupStepNumber: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.bold,
    fontWeight: theme.fontWeight.bold as any,
    color: theme.colors.primaryLight,
    marginRight: theme.spacing.sm,
    marginTop: 2,
  },
  setupStepContent: {
    flex: 1,
  },
  setupStepTitle: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.sm,
  },
  commandContainer: {
    backgroundColor: 'rgba(255, 255, 255, 0.04)',
    borderRadius: theme.borderRadius.md,
    borderWidth: 1,
    borderColor: withAlpha(theme.colors.primary, 0.2),
    padding: theme.spacing.md,
    alignItems: 'center',
    overflow: 'hidden',
  },
  commandContainerCopied: {
    backgroundColor: 'rgba(34, 197, 94, 0.1)',
    borderColor: 'rgba(34, 197, 94, 0.3)',
  },
  commandGlow: {
    borderRadius: theme.borderRadius.md,
  },
  commandText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.mono,
    color: theme.colors.primaryLight,
    marginBottom: theme.spacing.sm,
    textAlign: 'center',
  },
  copyLabel: {
    fontSize: 10,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.textMuted,
    textAlign: 'center',
    opacity: 0.8,
  },
  copyLabelCopied: {
    color: theme.colors.success,
  },
  completionMessage: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
    textAlign: 'center',
    marginTop: theme.spacing.lg,
    paddingHorizontal: theme.spacing.md,
  },
});
