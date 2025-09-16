/**
 * Omnara Color System (shared)
 *
 * Centralized color definitions for the entire application (web + mobile).
 * Keep this file framework-agnostic so both environments can consume it.
 */

// Import shared tokens directly to avoid circular deps with index.ts re-exports
import { brand as brandTokens, base as baseTokens } from '../tokens';

// Base color palette (consumes shared tokens where sensible)
export const colors = {
  // Brand colors - Ghibli-inspired warm palette
  brand: {
    'warm-midnight': '#2a1f3d',
    'cozy-amber': '#f59e0b',
    'soft-gold': '#fbbf24',
    'warm-charcoal': '#1a1618',
    'dusty-rose': '#c084fc',
    'sage-green': '#86efac',
    'terracotta': '#fb923c',
    'cream': '#fef3c7',
    'deep-navy': '#1e1b29',
    'retro-terminal': '#00ff00',
  },

  // Legacy colors - kept for backwards compatibility with old blue palette
  legacy: {
    'midnight-blue': brandTokens.primaryDark,
    'electric-blue': brandTokens.primary,
    'electric-accent': brandTokens.primaryLight,
    'electric-violet': brandTokens.secondary,
    'charcoal': baseTokens.charcoal,
    'off-white': baseTokens.offWhite,
  },

  // Omnara specific brand colors
  omnara: {
    gold: '#D4A574',
    'gold-light': '#E5B585',
    'cream-text': '#f5e6d3',
    'deep-blue': '#162856',
  },

  // Semantic colors
  semantic: {
    success: {
      50: '#f0fdf4',
      100: '#dcfce7',
      200: '#bbf7d0',
      300: '#86efac',
      400: '#4ade80',
      500: '#22c55e',
      600: '#16a34a',
      700: '#15803d',
      800: '#166534',
      900: '#14532d',
    },
    warning: {
      50: '#fffbeb',
      100: '#fef3c7',
      200: '#fde68a',
      300: '#fcd34d',
      400: '#fbbf24',
      500: '#f59e0b',
      600: '#d97706',
      700: '#b45309',
      800: '#92400e',
      900: '#78350f',
    },
    error: {
      50: '#fef2f2',
      100: '#fee2e2',
      200: '#fecaca',
      300: '#fca5a5',
      400: '#f87171',
      500: '#ef4444',
      600: '#dc2626',
      700: '#b91c1c',
      800: '#991b1b',
      900: '#7f1d1d',
    },
    info: {
      50: '#eff6ff',
      100: '#dbeafe',
      200: '#bfdbfe',
      300: '#93c5fd',
      400: '#60a5fa',
      500: '#3b82f6',
      600: '#2563eb',
      700: '#1d4ed8',
      800: '#1e40af',
      900: '#1e3a8a',
    },
  },

  // Neutral colors
  neutral: {
    50: '#fafafa',
    100: '#f5f5f5',
    200: '#e5e5e5',
    300: '#d4d4d4',
    400: '#a3a3a3',
    500: '#737373',
    600: '#525252',
    700: '#404040',
    800: '#262626',
    900: '#171717',
    950: '#0a0a0a',
  },

  // Special effect colors
  effects: {
    overlay: {
      light: 'rgba(255, 255, 255, 0.1)',
      medium: 'rgba(255, 255, 255, 0.2)',
      heavy: 'rgba(255, 255, 255, 0.3)',
    },
    shadow: {
      light: 'rgba(0, 0, 0, 0.1)',
      medium: 'rgba(0, 0, 0, 0.25)',
      heavy: 'rgba(0, 0, 0, 0.5)',
      dark: 'rgba(0, 0, 0, 0.75)',
    },
    amber: {
      glow: 'rgba(245, 158, 11, 0.2)',
      'glow-medium': 'rgba(245, 158, 11, 0.3)',
      'glow-heavy': 'rgba(245, 158, 11, 0.5)',
    },
    terminal: {
      green: 'rgba(0, 255, 0, 0.1)',
      'green-medium': 'rgba(0, 255, 0, 0.2)',
    },
    // Dashboard-specific overlay colors (matching original design)
    dashboard: {
      'vignette-light': 'rgba(26, 22, 24, 0.4)',
      'vignette-heavy': 'rgba(26, 22, 24, 0.7)',
      'top-fade': 'rgba(30, 27, 41, 0.7)',
      // Chat and UI element colors
      'chat-bg': 'rgba(255, 255, 255, 0.1)',
      'chat-border': 'rgba(255, 255, 255, 0.2)',
      'scrollbar-thumb': 'rgba(255, 255, 255, 0.1)',
      'scrollbar-thumb-hover': 'rgba(255, 255, 255, 0.2)',
      'textarea-scrollbar': 'rgba(60, 130, 247, 0.5)',
      'textarea-scrollbar-hover': 'rgba(96, 165, 250, 0.7)',
    },
  },
} as const;

// Semantic mappings for easier usage
export const semanticColors = {
  // Primary theme colors
  primary: colors.brand['cozy-amber'],
  'primary-foreground': colors.brand['warm-charcoal'],

  secondary: colors.brand['dusty-rose'],
  'secondary-foreground': colors.neutral[50],

  // Background colors
  background: colors.brand['deep-navy'],
  'background-alt': colors.brand['warm-midnight'],
  foreground: colors.neutral[50],

  // Interactive states
  success: colors.semantic.success[500],
  warning: colors.semantic.warning[500],
  error: colors.semantic.error[500],
  info: colors.semantic.info[500],

  // Special brand colors
  'omnara-gold': colors.omnara.gold,
  'claude-purple': '#D97757',
} as const;

// Gradient definitions
export const gradients = {
  // Brand gradients
  'warm-gradient': `linear-gradient(135deg, ${colors.brand['warm-midnight']} 0%, ${colors.brand['warm-charcoal']} 100%)`,
  'amber-glow': `radial-gradient(ellipse at center, ${colors.effects.amber.glow}, transparent 70%)`,
  'starfield': `radial-gradient(ellipse at bottom, ${colors.brand['deep-navy']} 0%, ${colors.brand['warm-midnight']} 100%)`,
  'terminal-glow': `radial-gradient(ellipse at center, ${colors.effects.terminal.green}, transparent 50%)`,

  // Legacy gradients
  'hero-gradient': `linear-gradient(to right, ${colors.legacy['midnight-blue']}, ${colors.legacy['electric-blue']})`,
  'card-gradient': `linear-gradient(135deg, ${colors.legacy['midnight-blue']} 0%, ${colors.legacy['electric-blue']} 100%)`,

  // Omnara specific gradients
  'omnara-card': `linear-gradient(135deg, rgba(42, 31, 61, 0.75) 0%, rgba(26, 22, 24, 0.7) 100%)`,
} as const;

// CSS custom property mappings (web can apply directly; mobile may ignore)
export const cssVariables = {
  // Base colors
  '--color-primary': semanticColors.primary,
  '--color-primary-foreground': semanticColors['primary-foreground'],
  '--color-secondary': semanticColors.secondary,
  '--color-secondary-foreground': semanticColors['secondary-foreground'],
  '--color-background': semanticColors.background,
  '--color-background-alt': semanticColors['background-alt'],
  '--color-foreground': semanticColors.foreground,

  // Semantic colors
  '--color-success': semanticColors.success,
  '--color-warning': semanticColors.warning,
  '--color-error': semanticColors.error,
  '--color-info': semanticColors.info,

  // Effects
  '--color-overlay-light': colors.effects.overlay.light,
  '--color-overlay-medium': colors.effects.overlay.medium,
  '--color-overlay-heavy': colors.effects.overlay.heavy,
  '--color-shadow-light': colors.effects.shadow.light,
  '--color-shadow-medium': colors.effects.shadow.medium,
  '--color-shadow-heavy': colors.effects.shadow.heavy,

  // Brand specific
  '--color-omnara-gold': colors.omnara.gold,
  '--color-claude-purple': '#D97757',
} as const;

// Export types for TypeScript
export type ColorToken =
  | keyof typeof colors.brand
  | keyof typeof colors.legacy
  | keyof typeof colors.omnara;
export type SemanticColorToken = keyof typeof semanticColors;
export type GradientToken = keyof typeof gradients;

