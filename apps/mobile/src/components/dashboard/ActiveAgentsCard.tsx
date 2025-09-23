import React from 'react';
import { View, Text, StyleSheet } from 'react-native';
import Animated, { 
  useAnimatedStyle, 
  useSharedValue,
  withSpring,
  withRepeat,
  withTiming,
  withSequence,
} from 'react-native-reanimated';
import { Bot } from 'lucide-react-native';
import { Card } from '@/components/ui';
import { theme } from '@/constants/theme';
import { withAlpha } from '@/lib/color';

interface ActiveAgentsCardProps {
  count: number;
  total: number;
}

export const ActiveAgentsCard: React.FC<ActiveAgentsCardProps> = ({ count, total }) => {
  const scale = useSharedValue(1);
  const rotation = useSharedValue(0);

  React.useEffect(() => {
    // Pulse animation for the emoji
    scale.value = withRepeat(
      withSequence(
        withTiming(1.1, { duration: 1000 }),
        withTiming(1, { duration: 1000 })
      ),
      -1,
      true
    );
    
    // Gentle rotation
    rotation.value = withRepeat(
      withSequence(
        withTiming(5, { duration: 2000 }),
        withTiming(-5, { duration: 2000 })
      ),
      -1,
      true
    );
  }, []);

  const animatedEmojiStyle = useAnimatedStyle(() => ({
    transform: [
      { scale: scale.value },
      { rotate: `${rotation.value}deg` },
    ],
  }));

  return (
    <View style={styles.cardContainer}>
      <View style={styles.card}>
        <View style={styles.content}>
          <View style={styles.leftSection}>
            <View style={styles.textSection}>
              <Text style={styles.count}>{count}</Text>
              <Text style={styles.label}>Active Agents</Text>
              <Text style={styles.sublabel}>All active instances</Text>
            </View>
          </View>
          <View style={styles.rightSection}>
            <Animated.View style={animatedEmojiStyle}>
              <Bot size={32} color={theme.colors.white} strokeWidth={1.5} />
            </Animated.View>
          </View>
        </View>
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  cardContainer: {
    marginHorizontal: theme.spacing.lg,
    marginBottom: theme.spacing.lg,
    borderRadius: theme.borderRadius.lg,
  },
  card: {
    backgroundColor: theme.colors.authContainer,
    borderRadius: theme.borderRadius.lg,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.1)',
    padding: theme.spacing.lg,
  },
  content: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
  },
  leftSection: {
    flex: 1,
    justifyContent: 'center',
  },
  textSection: {
    alignItems: 'flex-start',
  },
  count: {
    fontSize: 56,
    fontFamily: theme.fontFamily.bold,
    fontWeight: theme.fontWeight.bold,
    color: theme.colors.white,
    lineHeight: 56,
    marginBottom: theme.spacing.xs,
  },
  label: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium,
    color: theme.colors.white,
    marginBottom: theme.spacing.xs / 3,
  },
  sublabel: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.7)',
  },
  rightSection: {
    width: 64,
    height: 64,
    borderRadius: theme.borderRadius.full,
    backgroundColor: withAlpha(theme.colors.primary, 0.15),
    justifyContent: 'center',
    alignItems: 'center',
  },
});
