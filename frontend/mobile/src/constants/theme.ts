import { tokens, colors as sharedColors, semanticColors } from '@omnara/shared';
import { withAlpha } from '@/lib/color';

export const theme = {
  colors: {
    // Brand Colors (aligned with web)
    primary: semanticColors.primary,
    primaryLight: sharedColors.brand['soft-gold'],
    primaryDark: sharedColors.brand['warm-midnight'],
    secondary: semanticColors.secondary,
    secondaryLight: sharedColors.brand['dusty-rose'],
    secondaryDark: sharedColors.brand['warm-charcoal'],
    
    // Base Colors (aligned with web's sophisticated neutral palette)
    background: sharedColors.neutral[900],         // #171717 - matches web dark theme
    backgroundLight: sharedColors.legacy['off-white'],
    backgroundDark: sharedColors.neutral[950],     // #0a0a0a - deeper neutral
    surface: sharedColors.legacy['off-white'],
    surfaceDark: sharedColors.neutral[800],        // #262626 - elevated surface

    // Auth/Pane Colors (sophisticated neutral grays)
    authBackground: sharedColors.neutral[900],     // Main background
    authContainer: sharedColors.neutral[800],      // #262626 - card/panel background
    
    // Text Colors (improved contrast and readability)
    text: sharedColors.neutral[50],                // #fafafa - high contrast white
    textLight: sharedColors.neutral[300],          // #d4d4d4 - lighter secondary text
    textInverse: sharedColors.neutral[900],        // Dark text on light backgrounds
    textMuted: sharedColors.neutral[400],          // #a3a3a3 - muted but readable
    textSecondary: sharedColors.neutral[400],      // Consistent secondary text
    
    // Semantic Colors
    success: sharedColors.semantic.success[500],
    successLight: sharedColors.semantic.success[300],
    successDark: sharedColors.semantic.success[700],
    warning: sharedColors.semantic.warning[500],
    warningLight: sharedColors.semantic.warning[300],
    warningDark: sharedColors.semantic.warning[700],
    error: sharedColors.semantic.error[500],
    errorLight: sharedColors.semantic.error[300],
    errorDark: sharedColors.semantic.error[700],
    info: sharedColors.semantic.info[500],
    infoLight: sharedColors.semantic.info[300],
    infoDark: sharedColors.semantic.info[700],
    
    // Status Colors for Agents (subtle, matching web UI)
    status: {
      active: {
        bg: withAlpha(sharedColors.semantic.success[500], 0.15),  // More subtle
        border: withAlpha(sharedColors.semantic.success[400], 0.25),
        text: sharedColors.semantic.success[300],         // #86efac - softer green
      },
      awaiting_input: {
        bg: withAlpha(sharedColors.semantic.warning[500], 0.15),
        border: withAlpha(sharedColors.semantic.warning[400], 0.25),
        text: sharedColors.semantic.warning[300],         // #fcd34d - softer yellow
      },
      paused: {
        bg: withAlpha(sharedColors.semantic.info[500], 0.15),
        border: withAlpha(sharedColors.semantic.info[400], 0.25),
        text: sharedColors.semantic.info[300],            // #93c5fd - softer blue
      },
      stale: {
        bg: 'rgba(249, 115, 22, 0.15)',     // More subtle orange
        border: 'rgba(251, 146, 60, 0.25)',
        text: '#FCD34D',                     // Warmer orange text
      },
      failed: {
        bg: withAlpha(sharedColors.semantic.error[500], 0.15),
        border: withAlpha(sharedColors.semantic.error[400], 0.25),
        text: sharedColors.semantic.error[300],           // #fca5a5 - softer red
      },
      completed: {
        bg: withAlpha(sharedColors.neutral[500], 0.15),   // More subtle gray
        border: withAlpha(sharedColors.neutral[400], 0.25),
        text: sharedColors.neutral[300],                  // #d4d4d4 - readable gray
      },
      killed: {
        bg: withAlpha(sharedColors.semantic.error[500], 0.15),
        border: withAlpha(sharedColors.semantic.error[400], 0.25),
        text: sharedColors.semantic.error[300],
      },
    },
    
    // Pro/Premium Colors
    pro: sharedColors.omnara.gold,
    proDark: sharedColors.omnara['gold-light'],
    
    // Utility Colors
    white: '#FFFFFF',
    black: '#000000',
    transparent: 'transparent',

    // UI Element Colors (matching web UI)
    border: 'rgba(255, 255, 255, 0.08)',          // Subtle borders like web
    borderLight: 'rgba(255, 255, 255, 0.12)',     // Slightly more visible
    borderDivider: 'rgba(255, 255, 255, 0.05)',   // Very subtle dividers
    cardSurface: sharedColors.neutral[800],        // Card backgrounds
    panelSurface: 'rgba(38, 38, 38, 0.8)',  // Elevated panels (custom neutral-850)
    
    // Glass Morphism
    glass: {
      white: 'rgba(255, 255, 255, 0.1)',
      dark: 'rgba(0, 0, 0, 0.1)',
      primary: 'rgba(245, 158, 11, 0.1)',
    },
  },
  spacing: {
    xs: 4,
    sm: 8,
    md: 16,
    lg: 24,
    xl: 32,
    xxl: 48,
    '3xl': 64,
  },
  borderRadius: {
    sm: 8,
    md: 12,
    lg: 16,
    xl: 24,
    full: 9999,
  },
  fontSize: {
    xs: 12,
    sm: 14,
    base: 16,
    lg: 18,
    xl: 20,
    '2xl': 24,
    '3xl': 30,
    '4xl': 36,
  },
  fontWeight: {
    light: '300' as const,
    normal: '400' as const,
    medium: '500' as const,
    semibold: '600' as const,
    bold: '700' as const,
    extrabold: '800' as const,
    black: '900' as const,
  },
  fontFamily: {
    light: 'Inter-Light',
    lightItalic: 'Inter-Light-Italic',
    regular: 'Inter-Regular',
    regularItalic: 'Inter-Regular-Italic',
    medium: 'Inter-Medium',
    semibold: 'Inter-SemiBold',
    bold: 'Inter-Bold',
    extrabold: 'Inter-ExtraBold',
    black: 'Inter-Black',
    mono: 'Courier-Regular',
  },
  shadow: {
    sm: {
      shadowColor: '#000',
      shadowOffset: { width: 0, height: 1 },
      shadowOpacity: 0.05,
      shadowRadius: 2,
      elevation: 1,
    },
    md: {
      shadowColor: '#000',
      shadowOffset: { width: 0, height: 2 },
      shadowOpacity: 0.1,
      shadowRadius: 3.84,
      elevation: 3,
    },
    lg: {
      shadowColor: '#000',
      shadowOffset: { width: 0, height: 4 },
      shadowOpacity: 0.15,
      shadowRadius: 5.46,
      elevation: 5,
    },
    xl: {
      shadowColor: '#000',
      shadowOffset: { width: 0, height: 6 },
      shadowOpacity: 0.2,
      shadowRadius: 8.3,
      elevation: 8,
    },
    '2xl': {
      shadowColor: '#000',
      shadowOffset: { width: 0, height: 10 },
      shadowOpacity: 0.25,
      shadowRadius: 12,
      elevation: 12,
    },
    glow: {
      primary: {
        shadowColor: semanticColors.primary,
        shadowOffset: { width: 0, height: 0 },
        shadowOpacity: 0.3,
        shadowRadius: 20,
        elevation: 8,
      },
      secondary: {
        shadowColor: '#8B5CF6',
        shadowOffset: { width: 0, height: 0 },
        shadowOpacity: 0.3,
        shadowRadius: 20,
        elevation: 8,
      },
    },
  },
} as const;

export type Theme = typeof theme;
