import React from 'react';
import { LinearGradient } from 'expo-linear-gradient';
import { StyleSheet, ViewProps } from 'react-native';
import { theme } from '@/constants/theme';

interface GradientProps extends ViewProps {
  variant?: 'primary' | 'secondary' | 'dark' | 'light' | 'hero' | 'card' | 'aurora';
  angle?: number;
  children?: React.ReactNode;
  colors?: string[];
  locations?: number[];
}

const gradientColors: Record<string, readonly string[]> = {
  primary: [theme.colors.primaryDark, theme.colors.primary, theme.colors.primaryLight],
  secondary: [theme.colors.secondaryDark, theme.colors.secondary, theme.colors.secondaryLight],
  dark: [theme.colors.backgroundDark, theme.colors.background],
  light: [theme.colors.surface, theme.colors.backgroundLight],
  hero: [theme.colors.primaryDark, theme.colors.primary],
  card: [theme.colors.primaryDark, theme.colors.primary],
  aurora: [
    theme.colors.primaryDark,
    theme.colors.primary,
    theme.colors.secondary,
    theme.colors.primaryLight,
  ],
};

export const Gradient: React.FC<GradientProps> = ({
  variant = 'primary',
  angle = 135,
  style,
  children,
  colors: customColors,
  locations,
  ...props
}) => {
  const baseColors = customColors || gradientColors[variant];
  // Ensure colors is properly typed for LinearGradient
  const colors = Array.isArray(baseColors) ? baseColors : [baseColors];
  
  // Convert angle to start/end points
  const angleRad = (angle * Math.PI) / 180;
  const start = {
    x: 0.5 - Math.sin(angleRad) * 0.5,
    y: 0.5 + Math.cos(angleRad) * 0.5,
  };
  const end = {
    x: 0.5 + Math.sin(angleRad) * 0.5,
    y: 0.5 - Math.cos(angleRad) * 0.5,
  };

  // Ensure we have at least 2 colors for gradient
  const finalColors = colors.length < 2 ? [...colors, colors[0]] : colors;

  return (
    <LinearGradient
      colors={finalColors as [string, string, ...string[]]}
      start={start}
      end={end}
      locations={locations as [number, number, ...number[]] | undefined}
      style={[styles.gradient, style]}
      {...props}
    >
      {children}
    </LinearGradient>
  );
};

const styles = StyleSheet.create({
  gradient: {
    flex: 1,
  },
});