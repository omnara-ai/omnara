import React from 'react';
import {
  View,
  StyleSheet,
  ViewProps,
  Pressable,
  PressableProps,
  Animated,
} from 'react-native';
import { BlurView } from 'expo-blur';
import { LinearGradient } from 'expo-linear-gradient';
import { theme } from '@/constants/theme';
import { withAlpha } from '@/lib/color';

interface CardProps {
  variant?: 'default' | 'glass' | 'outlined' | 'gradient';
  pressable?: boolean;
  onPress?: () => void;
  children: React.ReactNode;
  glowColor?: 'primary' | 'secondary';
  elevation?: 'sm' | 'md' | 'lg' | 'xl' | '2xl';
  style?: ViewProps['style'];
}

export const Card: React.FC<CardProps> = ({
  variant = 'default',
  pressable = false,
  onPress,
  style,
  children,
  glowColor,
  elevation = 'md',
}) => {
  const scaleAnim = React.useRef(new Animated.Value(1)).current;

  const handlePressIn = () => {
    Animated.spring(scaleAnim, {
      toValue: 0.98,
      useNativeDriver: true,
      tension: 100,
      friction: 10,
    }).start();
  };

  const handlePressOut = () => {
    Animated.spring(scaleAnim, {
      toValue: 1,
      useNativeDriver: true,
      tension: 100,
      friction: 10,
    }).start();
  };

  const content = (
    <>
      {variant === 'glass' && (
        <>
          {/* Main glassmorphism background */}
          <View style={[StyleSheet.absoluteFillObject, styles.glassBackground]} />
          {/* Subtle highlight overlay */}
          <LinearGradient
            colors={[
              'rgba(255, 255, 255, 0.15)',
              'rgba(255, 255, 255, 0.05)',
              'rgba(255, 255, 255, 0)',
            ]}
            start={{ x: 0, y: 0 }}
            end={{ x: 0, y: 0.3 }}
            style={[StyleSheet.absoluteFillObject, styles.glassHighlight]}
          />
        </>
      )}
      {variant === 'gradient' && (
        <LinearGradient
          colors={[theme.colors.primaryDark, theme.colors.primary]}
          start={{ x: 0, y: 0 }}
          end={{ x: 1, y: 1 }}
          style={StyleSheet.absoluteFillObject}
        />
      )}
      <View style={[styles.content, variant === 'glass' && styles.glassContent]}>
        {children}
      </View>
    </>
  );

  const elevationStyle = elevation ? theme.shadow[elevation] : undefined;
  const glowStyle = glowColor ? theme.shadow.glow[glowColor] : undefined;

  const cardStyle = [
    styles.base,
    styles[variant],
    elevationStyle,
    glowStyle,
    style,
  ];

  if (pressable && onPress) {
    return (
      <Animated.View style={{ transform: [{ scale: scaleAnim }] }}>
        <Pressable
          onPress={onPress}
          onPressIn={handlePressIn}
          onPressOut={handlePressOut}
          style={cardStyle}
        >
          {content}
        </Pressable>
      </Animated.View>
    );
  }

  return (
    <View style={cardStyle}>
      {content}
    </View>
  );
};

const styles = StyleSheet.create({
  base: {
    borderRadius: theme.borderRadius.lg,
    overflow: 'hidden',
  },
  content: {
    padding: theme.spacing.md,
  },
  glassContent: {
    backgroundColor: 'transparent',
  },
  glassBackground: {
    backgroundColor: withAlpha(theme.colors.primary, 0.15),
  },
  glassHighlight: {
    borderTopLeftRadius: theme.borderRadius.lg,
    borderTopRightRadius: theme.borderRadius.lg,
  },
  // Variants
  default: {
    backgroundColor: theme.colors.surface,
  },
  glass: {
    backgroundColor: 'transparent',
    borderWidth: 1.5,
    borderColor: 'rgba(255, 255, 255, 0.3)', // Light translucent border
  },
  gradient: {
    backgroundColor: 'transparent',
  },
  outlined: {
    backgroundColor: theme.colors.surface,
    borderWidth: 1,
    borderColor: theme.colors.textLight,
    shadowOpacity: 0,
    elevation: 0,
  },
});
