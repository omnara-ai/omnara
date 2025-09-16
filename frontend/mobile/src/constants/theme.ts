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
    
    // Base Colors
    background: semanticColors.background,         // Deep navy (web background)
    backgroundLight: sharedColors.legacy['off-white'],
    backgroundDark: semanticColors['background-alt'],
    surface: sharedColors.legacy['off-white'],
    surfaceDark: sharedColors.brand['warm-midnight'],
    
    // Auth/Pane Colors (aligned with web palette)
    authBackground: semanticColors.background,
    authContainer: sharedColors.brand['warm-midnight'],
    
    // Text Colors
    text: semanticColors.foreground,
    textLight: sharedColors.neutral[400],
    textInverse: sharedColors.neutral[50],
    textMuted: sharedColors.neutral[500],
    
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
    
    // Status Colors for Agents
    status: {
      active: {
        bg: 'rgba(34, 197, 94, 0.2)',      // green-500/20
        border: 'rgba(74, 222, 128, 0.3)', // green-400/30
        text: '#BBF7D0',                    // green-200
      },
      awaiting_input: {
        bg: 'rgba(234, 179, 8, 0.2)',      // yellow-500/20
        border: 'rgba(250, 204, 21, 0.3)', // yellow-400/30
        text: '#FEF08A',                    // yellow-200
      },
      paused: {
        bg: withAlpha(sharedColors.semantic.info[500], 0.2),
        border: withAlpha(sharedColors.semantic.info[500], 0.3),
        text: sharedColors.semantic.info[200],
      },
      stale: {
        bg: 'rgba(249, 115, 22, 0.2)',     // orange-500/20
        border: 'rgba(251, 146, 60, 0.3)', // orange-400/30
        text: '#FED7AA',                    // orange-200
      },
      failed: {
        bg: 'rgba(239, 68, 68, 0.2)',      // red-500/20
        border: 'rgba(248, 113, 113, 0.3)',// red-400/30
        text: '#FECACA',                    // red-200
      },
      completed: {
        bg: 'rgba(107, 114, 128, 0.2)',    // gray-500/20
        border: 'rgba(156, 163, 175, 0.3)',// gray-400/30
        text: '#E5E7EB',                    // gray-200
      },
      killed: {
        bg: 'rgba(239, 68, 68, 0.2)',      // red-500/20
        border: 'rgba(248, 113, 113, 0.3)',// red-400/30
        text: '#FECACA',                    // red-200
      },
    },
    
    // Pro/Premium Colors
    pro: sharedColors.omnara.gold,
    proDark: sharedColors.omnara['gold-light'],
    
    // Utility Colors
    white: '#FFFFFF',
    black: '#000000',
    transparent: 'transparent',
    
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
