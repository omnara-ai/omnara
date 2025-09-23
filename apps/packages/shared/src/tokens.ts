// Shared design tokens for Omnara (web + mobile)
// Keep this file framework-agnostic.

export const brand = {
  // Core brand hues used across apps
  primary: '#3B82F6',
  primaryLight: '#60A5FA',
  primaryDark: '#1E3A8A',
  secondary: '#8B5CF6',
  secondaryLight: '#A78BFA',
  secondaryDark: '#6D28D9',
} as const;

export const base = {
  backgroundDark: '#0C134F',
  backgroundLight: '#F9FAFB',
  charcoal: '#111827',
  offWhite: '#F9FAFB',
  white: '#FFFFFF',
  black: '#000000',
} as const;

export const spacing = {
  xs: 4,
  sm: 8,
  md: 16,
  lg: 24,
  xl: 32,
  xxl: 48,
} as const;

export type Brand = typeof brand;
export type Base = typeof base;
export type Spacing = typeof spacing;

