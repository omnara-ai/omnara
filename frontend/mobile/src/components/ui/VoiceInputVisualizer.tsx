import React, { useEffect, useRef } from 'react';
import { View, Text, StyleSheet, Animated } from 'react-native';
import { theme } from '@/constants/theme';

interface VoiceInputVisualizerProps {
  volumeLevel: number; // 0-1
  isRecording: boolean;
  liveTranscript?: string;
  duration?: number;
}

export const VoiceInputVisualizer: React.FC<VoiceInputVisualizerProps> = ({
  volumeLevel,
  isRecording,
  liveTranscript,
  duration = 0,
}) => {
  const barAnimations = useRef([
    new Animated.Value(0.2),
    new Animated.Value(0.2),
    new Animated.Value(0.2),
    new Animated.Value(0.2),
  ]).current;
  const animationTimer = useRef<NodeJS.Timeout | null>(null);

  useEffect(() => {
    if (!isRecording) {
      if (animationTimer.current) {
        clearInterval(animationTimer.current);
        animationTimer.current = null;
      }

      barAnimations.forEach(anim => {
        Animated.timing(anim, {
          toValue: 0.2,
          duration: 200,
          useNativeDriver: true,
        }).start();
      });
      return;
    }

    if (animationTimer.current) {
      clearInterval(animationTimer.current);
      animationTimer.current = null;
    }

    if (volumeLevel > 0.01) {
      barAnimations.forEach((anim, index) => {
        const delay = index * 30;
        const randomFactor = 0.8 + (Math.random() * 0.4);
        const targetHeight = Math.min(1, volumeLevel * randomFactor);

        Animated.timing(anim, {
          toValue: Math.max(0.15, targetHeight),
          duration: 100,
          delay: delay,
          useNativeDriver: true,
        }).start();
      });
    } else {
      let step = 0;
      animationTimer.current = setInterval(() => {
        step += 1;
        barAnimations.forEach((anim, index) => {
          const phase = (step / 8) + (index * Math.PI / 3);
          const targetHeight = 0.2 + Math.sin(phase) * 0.1;

          Animated.timing(anim, {
            toValue: targetHeight,
            duration: 300,
            useNativeDriver: true,
          }).start();
        });
      }, 300);
    }

    return () => {
      if (animationTimer.current) {
        clearInterval(animationTimer.current);
        animationTimer.current = null;
      }
    };
  }, [volumeLevel, isRecording]);

  const formatDuration = (seconds: number) => {
    const mins = Math.floor(seconds / 60);
    const secs = seconds % 60;
    return `${mins}:${secs.toString().padStart(2, '0')}`;
  };

  if (!isRecording) return null;

  return (
    <View style={styles.container}>
      <View style={styles.visualizerContainer}>
        <View style={styles.barsContainer}>
          {barAnimations.map((anim, index) => (
            <Animated.View
              key={index}
              style={[
                styles.bar,
                {
                  transform: [{ scaleY: anim }],
                  opacity: isRecording ? 1 : 0.3,
                },
              ]}
            />
          ))}
        </View>

        <View style={styles.textContainer}>
          {liveTranscript ? (
            <Text style={styles.transcriptText}>
              {liveTranscript}
            </Text>
          ) : (
            <Text style={styles.listeningText}>
              Listening... {formatDuration(duration)}
            </Text>
          )}
        </View>
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center', // Center everything vertically
    paddingLeft: theme.spacing.md,
    paddingRight: theme.spacing.sm,
    minHeight: 42, // Match input container minHeight
  },
  visualizerContainer: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    gap: theme.spacing.sm,
  },
  textContainer: {
    flex: 1,
  },
  barsContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 3,
    height: 20,
  },
  bar: {
    width: 3,
    height: 20,
    backgroundColor: theme.colors.primary,
    borderRadius: 1.5,
  },
  transcriptText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.text,
    lineHeight: 18,
    textAlignVertical: 'center',
  },
  listeningText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.textMuted,
  },
});