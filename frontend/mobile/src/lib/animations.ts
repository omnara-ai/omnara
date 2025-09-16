import { Animated, Easing } from 'react-native';

// Animation durations from web app
export const ANIMATION_DURATION = {
  fast: 200,
  normal: 300,
  slow: 500,
  verySlow: 1000,
  // Complex animation durations from web
  lightBeam1: 20700,
  lightBeam2: 25300,
  lightBeam3: 29900,
  aurora: [12000, 14000, 16000, 18000, 20000],
};

// Spring animations for interactive elements
export const springConfig = {
  default: {
    tension: 100,
    friction: 10,
    useNativeDriver: true,
  },
  bouncy: {
    tension: 180,
    friction: 8,
    useNativeDriver: true,
  },
  stiff: {
    tension: 200,
    friction: 15,
    useNativeDriver: true,
  },
};

// Timing animations
export const timingConfig = {
  default: {
    duration: ANIMATION_DURATION.normal,
    easing: Easing.out(Easing.cubic),
    useNativeDriver: true,
  },
  fast: {
    duration: ANIMATION_DURATION.fast,
    easing: Easing.out(Easing.cubic),
    useNativeDriver: true,
  },
  slow: {
    duration: ANIMATION_DURATION.slow,
    easing: Easing.inOut(Easing.cubic),
    useNativeDriver: true,
  },
};

// Create floating animation (similar to web's floating effect)
export const createFloatingAnimation = (
  animatedValue: Animated.Value,
  duration: number = 3000
) => {
  return Animated.loop(
    Animated.sequence([
      Animated.timing(animatedValue, {
        toValue: 1,
        duration: duration / 2,
        easing: Easing.inOut(Easing.ease),
        useNativeDriver: true,
      }),
      Animated.timing(animatedValue, {
        toValue: 0,
        duration: duration / 2,
        easing: Easing.inOut(Easing.ease),
        useNativeDriver: true,
      }),
    ])
  );
};

// Create pulse animation
export const createPulseAnimation = (
  animatedValue: Animated.Value,
  minScale: number = 0.95,
  maxScale: number = 1.05
) => {
  return Animated.loop(
    Animated.sequence([
      Animated.timing(animatedValue, {
        toValue: maxScale,
        duration: 1000,
        easing: Easing.inOut(Easing.ease),
        useNativeDriver: true,
      }),
      Animated.timing(animatedValue, {
        toValue: minScale,
        duration: 1000,
        easing: Easing.inOut(Easing.ease),
        useNativeDriver: true,
      }),
    ])
  );
};

// Create fade in animation
export const createFadeInAnimation = (
  animatedValue: Animated.Value,
  delay: number = 0
) => {
  return Animated.timing(animatedValue, {
    toValue: 1,
    duration: ANIMATION_DURATION.normal,
    delay,
    easing: Easing.out(Easing.cubic),
    useNativeDriver: true,
  });
};

// Create slide and fade animation
export const createSlideInAnimation = (
  animatedValue: Animated.Value,
  direction: 'up' | 'down' | 'left' | 'right' = 'up',
  distance: number = 20
) => {
  const translateKey = direction === 'up' || direction === 'down' ? 'translateY' : 'translateX';
  const multiplier = direction === 'up' || direction === 'left' ? 1 : -1;
  
  return {
    opacity: animatedValue,
    transform: [{
      [translateKey]: animatedValue.interpolate({
        inputRange: [0, 1],
        outputRange: [distance * multiplier, 0],
      }),
    }],
  };
};

// Light beam animation helper (simplified for React Native)
export const createLightBeamAnimation = (
  animatedValue: Animated.Value,
  duration: number
) => {
  return Animated.loop(
    Animated.timing(animatedValue, {
      toValue: 1,
      duration,
      easing: Easing.linear,
      useNativeDriver: true,
    })
  );
};

// Shimmer effect for loading states
export const createShimmerAnimation = (animatedValue: Animated.Value) => {
  return Animated.loop(
    Animated.timing(animatedValue, {
      toValue: 1,
      duration: 1500,
      easing: Easing.linear,
      useNativeDriver: true,
    })
  );
};

// Status indicator pulse for active agents
export const createStatusPulse = (animatedValue: Animated.Value) => {
  return Animated.loop(
    Animated.sequence([
      Animated.timing(animatedValue, {
        toValue: 1.2,
        duration: 1000,
        easing: Easing.out(Easing.ease),
        useNativeDriver: true,
      }),
      Animated.timing(animatedValue, {
        toValue: 1,
        duration: 1000,
        easing: Easing.in(Easing.ease),
        useNativeDriver: true,
      }),
    ])
  );
};

// Export animation utilities
export const AnimationUtils = {
  spring: (value: Animated.Value, toValue: number, config = springConfig.default) => {
    return Animated.spring(value, { ...config, toValue });
  },
  
  timing: (value: Animated.Value, toValue: number, config = timingConfig.default) => {
    return Animated.timing(value, { ...config, toValue });
  },
  
  parallel: (animations: Animated.CompositeAnimation[]) => {
    return Animated.parallel(animations);
  },
  
  sequence: (animations: Animated.CompositeAnimation[]) => {
    return Animated.sequence(animations);
  },
  
  delay: (duration: number) => {
    return Animated.delay(duration);
  },
};