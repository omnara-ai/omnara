Shared design system for Omnara.

- Exported modules:
  - `tokens` — low-level brand/base/spacing tokens (framework-agnostic)
  - `theme/colors` — app-level palette, semantic colors, gradients, and CSS variable map

Usage examples:

- Web (Tailwind, components):
  - Import from `apps/web/src/lib/theme/colors` (re-exports shared for Tailwind/Jiti compatibility)

- Mobile (React Native):
  - `import { colors, semanticColors } from '@omnara/shared'`

