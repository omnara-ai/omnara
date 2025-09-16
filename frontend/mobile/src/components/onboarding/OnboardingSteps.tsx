import React, { useState } from 'react';
import {
  View,
  Text,
  StyleSheet,
  TouchableOpacity,
  Alert,
} from 'react-native';
import { LinearGradient } from 'expo-linear-gradient';
import { Plus, Terminal } from 'lucide-react-native';
import { theme } from '@/constants/theme';
import * as Clipboard from 'expo-clipboard';
import { withAlpha } from '@/lib/color';

interface OnboardingStepsProps {
  onAddAgent: () => void;
  agentAdded?: boolean;
}

export const OnboardingSteps: React.FC<OnboardingStepsProps> = ({ 
  onAddAgent,
  agentAdded = false 
}) => {
  const handleCopyCommand = () => {
    const command = 'pipx run --no-cache omnara --claude-code-webhook --cloudflare-tunnel';
    Clipboard.setStringAsync(command);
    Alert.alert('Copied!', 'Command copied to clipboard.');
  };

  return (
    <View style={styles.container}>
      <Terminal size={48} color={theme.colors.primaryLight} strokeWidth={1.5} />
      
      <Text style={styles.title}>Get Started with Claude Code</Text>
      
      <Text style={styles.description}>
        Follow these steps to connect Claude Code and talk to it from anywhere.
      </Text>

      <View style={styles.setupSection}>
        <Text style={styles.setupStepNumber}>1</Text>
        <View style={styles.setupStepContent}>
          <Text style={styles.setupStepTitle}>Open a terminal on your computer</Text>
          <Text style={styles.setupStepDescription}>
            Navigate to the git directory you want to work in, then run:
          </Text>
          
          <TouchableOpacity 
            style={styles.commandContainer}
            onPress={handleCopyCommand}
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
              pipx run --no-cache omnara --claude-code-webhook --cloudflare-tunnel
            </Text>
            <View style={styles.copyIcon}>
              <Text style={styles.copyLabel}>TAP TO COPY</Text>
            </View>
          </TouchableOpacity>
        </View>
      </View>

      <View style={styles.setupSection}>
        <Text style={styles.setupStepNumber}>2</Text>
        <View style={styles.setupStepContent}>
          <Text style={styles.setupStepTitle}>Add your agent</Text>
          <Text style={styles.setupStepDescription}>
            Click the button below and paste the output from your terminal into the agent config.
          </Text>
          
          <TouchableOpacity
            onPress={onAddAgent}
            style={[
              styles.addAgentButton,
              agentAdded && styles.addAgentButtonSuccess
            ]}
            activeOpacity={0.7}
          >
            <Plus size={20} color={theme.colors.white} strokeWidth={2} />
            <Text style={styles.addAgentButtonText}>
              {agentAdded ? 'Agent Added!' : 'Add Agent'}
            </Text>
          </TouchableOpacity>
        </View>
      </View>

      <View style={styles.setupSection}>
        <Text style={styles.setupStepNumber}>3</Text>
        <View style={styles.setupStepContent}>
          <Text style={styles.setupStepTitle}>Start chatting</Text>
          <Text style={styles.setupStepDescription}>
            Once the agent is created, click the + button to talk to Claude Code from anywhere!
          </Text>
        </View>
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    alignItems: 'center',
    width: '100%',
  },
  title: {
    fontSize: theme.fontSize.xl,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    textAlign: 'center',
    marginTop: theme.spacing.lg,
    marginBottom: theme.spacing.sm,
  },
  description: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
    textAlign: 'center',
    marginBottom: theme.spacing.xl,
  },
  setupSection: {
    width: '100%',
    flexDirection: 'row',
    alignItems: 'flex-start',
    marginBottom: theme.spacing.lg,
  },
  setupStepNumber: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.bold,
    fontWeight: theme.fontWeight.bold as any,
    color: theme.colors.primaryLight,
    marginRight: theme.spacing.md,
    marginTop: 2,
  },
  setupStepContent: {
    flex: 1,
  },
  setupStepTitle: {
    fontSize: theme.fontSize.lg,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.xs,
  },
  setupStepDescription: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
    marginBottom: theme.spacing.md,
  },
  commandContainer: {
    backgroundColor: 'rgba(255, 255, 255, 0.04)',
    borderRadius: theme.borderRadius.md,
    borderWidth: 1,
    borderColor: withAlpha(theme.colors.primary, 0.2),
    padding: theme.spacing.md,
    marginBottom: theme.spacing.sm,
    overflow: 'hidden',
  },
  commandGlow: {
    borderRadius: theme.borderRadius.md,
  },
  commandText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.mono,
    color: theme.colors.primaryLight,
    marginBottom: theme.spacing.xs,
  },
  copyIcon: {
    alignSelf: 'flex-end',
  },
  copyLabel: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.textMuted,
  },
  addAgentButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: 'rgba(255, 255, 255, 0.1)',
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.2)',
    borderRadius: theme.borderRadius.md,
    paddingHorizontal: theme.spacing.xl,
    paddingVertical: theme.spacing.md,
    minHeight: 44,
    gap: theme.spacing.sm,
  },
  addAgentButtonSuccess: {
    backgroundColor: 'rgba(34, 197, 94, 0.2)',
    borderColor: 'rgba(34, 197, 94, 0.3)',
  },
  addAgentButtonText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
  },
});
