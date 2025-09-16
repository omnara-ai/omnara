// Re-export shared theme so both web and mobile consume a single source of truth.
// Use a relative path so Tailwind/Jiti can resolve this in Node context.
export {
  colors,
  gradients,
  cssVariables,
  semanticColors,
  type ColorToken,
  type SemanticColorToken,
  type GradientToken,
} from '../../../../packages/shared/src/theme/colors';
