import React from 'react';
import { View, Text, StyleSheet } from 'react-native';
import { LinearGradient } from 'expo-linear-gradient';
import { Check, LucideIcon } from 'lucide-react-native';
import { theme } from '@/constants/theme';

interface Feature {
  text: string;
  included: boolean;
}

interface PlanCardProps {
  title: string;
  subtitle: string;
  icon: LucideIcon;
  iconColor: string;
  iconBackgroundColor: string;
  gradientColors: [string, string];
  borderColor: string;
  features: Feature[];
  additionalInfo?: string;
}

export const PlanCard: React.FC<PlanCardProps> = ({
  title,
  subtitle,
  icon: Icon,
  iconColor,
  iconBackgroundColor,
  gradientColors,
  borderColor,
  features,
  additionalInfo,
}) => {
  return (
    <View style={[styles.container, { borderColor }]}>
      <LinearGradient
        colors={gradientColors}
        start={{ x: 0, y: 0 }}
        end={{ x: 0, y: 1 }}
        style={styles.gradient}
      >
        <View style={styles.header}>
          <View style={[styles.iconContainer, { backgroundColor: iconBackgroundColor }]}>
            <Icon 
              size={24} 
              color={iconColor} 
              strokeWidth={2}
            />
          </View>
          <View style={styles.titleContainer}>
            <Text style={styles.title}>{title}</Text>
            <Text style={styles.subtitle}>{subtitle}</Text>
          </View>
        </View>

        <View style={styles.features}>
          {features.map((feature, index) => (
            <View key={index} style={styles.featureRow}>
              <Text style={styles.featureText}>{feature.text}</Text>
              <Check 
                size={16} 
                color={feature.included ? iconColor : 'rgba(255, 255, 255, 0.3)'} 
                strokeWidth={feature.included ? 2 : 1}
              />
            </View>
          ))}
        </View>

        {additionalInfo && (
          <View style={styles.additionalInfo}>
            <Text style={styles.additionalInfoText}>{additionalInfo}</Text>
          </View>
        )}
      </LinearGradient>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    marginHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.lg,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    overflow: 'hidden',
  },
  gradient: {
    padding: theme.spacing.lg,
    borderRadius: theme.borderRadius.lg,
  },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
  },
  iconContainer: {
    width: 48,
    height: 48,
    borderRadius: theme.borderRadius.full,
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: theme.spacing.md,
  },
  titleContainer: {
    flex: 1,
  },
  title: {
    fontSize: theme.fontSize.xl,
    fontFamily: theme.fontFamily.semibold,
    fontWeight: theme.fontWeight.semibold as any,
    color: theme.colors.white,
    marginBottom: theme.spacing.xs / 2,
  },
  subtitle: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.7)',
  },
  features: {
    marginTop: theme.spacing.lg,
    paddingTop: theme.spacing.lg,
    borderTopWidth: 1,
    borderTopColor: 'rgba(255, 255, 255, 0.1)',
  },
  featureRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: theme.spacing.sm,
  },
  featureText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.7)',
    flex: 1,
    marginRight: theme.spacing.sm,
  },
  additionalInfo: {
    marginTop: theme.spacing.md,
    paddingTop: theme.spacing.md,
    borderTopWidth: 1,
    borderTopColor: 'rgba(255, 255, 255, 0.1)',
  },
  additionalInfoText: {
    fontSize: theme.fontSize.xs,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.5)',
    textAlign: 'center',
  },
});