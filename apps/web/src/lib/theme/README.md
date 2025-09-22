# Omnara Color System Documentation

This documentation outlines the centralized color system for the Omnara application. All colors and themes are now managed through a single source of truth.

## Overview

The Omnara color system consists of:
- **Centralized color definitions** in `/src/lib/theme/colors.ts`
- **Theme provider** for runtime theme management
- **CSS custom properties** for consistent styling
- **Tailwind integration** for utility classes

## File Structure

```
src/lib/theme/
├── colors.ts          # Centralized color definitions
├── ThemeProvider.tsx  # React context for theme management
├── utils.ts          # Color utility functions
└── README.md         # This documentation
```

## Usage Guide

### 1. Using Colors in Components

#### CSS Custom Properties (Recommended)
```css
/* Use semantic color tokens */
background: hsl(var(--primary));
color: hsl(var(--foreground));
border: 1px solid hsl(var(--border));

/* Use with opacity */
background: hsl(var(--primary) / 0.5);
```

#### Tailwind Classes
```tsx
// Semantic colors
<div className="bg-primary text-primary-foreground">
<div className="bg-background text-foreground">

// Brand colors
<div className="bg-cozy-amber text-warm-charcoal">
<div className="bg-omnara-gold text-black">

// State colors
<div className="bg-success-500 text-white">
<div className="bg-error-500 text-error-50">
```

#### JavaScript/TypeScript
```tsx
import { colors, semanticColors } from '@/lib/theme/colors';

// Use brand colors
const primaryColor = colors.brand['cozy-amber'];

// Use semantic colors
const textColor = semanticColors.foreground;
```

### 2. Theme Management

#### Setup Theme Provider
```tsx
// In your main App component
import { ThemeProvider } from '@/lib/theme/ThemeProvider';

function App() {
  return (
    <ThemeProvider defaultTheme="dark">
      <YourAppContent />
    </ThemeProvider>
  );
}
```

#### Using Theme Context
```tsx
import { useTheme } from '@/lib/theme/ThemeProvider';

function MyComponent() {
  const { theme, setTheme, toggleTheme } = useTheme();
  
  return (
    <button onClick={toggleTheme}>
      Current theme: {theme}
    </button>
  );
}
```

### 3. Color Categories

#### Brand Colors (Ghibli-inspired)
- `cozy-amber` (#f59e0b) - Primary brand color
- `soft-gold` (#fbbf24) - Secondary accent
- `warm-midnight` (#2a1f3d) - Dark background
- `dusty-rose` (#c084fc) - Special elements
- `sage-green` (#86efac) - Success states
- `terracotta` (#fb923c) - Warning states
- `cream` (#fef3c7) - Light accents
- `deep-navy` (#1e1b29) - Main background
- `warm-charcoal` (#1a1618) - Dark surfaces

#### Legacy Colors (Backwards Compatibility)
- `midnight-blue` (#1E3A8A) - Legacy primary
- `electric-blue` (#3B82F6) - Legacy accent
- `electric-accent` (#60A5FA) - Legacy highlight
- `electric-violet` (#8B5CF6) - Legacy secondary

#### Semantic Colors
```typescript
// Success states
success-50 to success-900

// Warning states  
warning-50 to warning-900

// Error states
error-50 to error-900

// Info states
info-50 to info-900

// Neutral grays
neutral-50 to neutral-950
```

### 4. Best Practices

#### ✅ DO
- Use CSS custom properties for consistent theming
- Use semantic color tokens (`--primary`, `--foreground`) over specific colors
- Use Tailwind classes with centralized colors
- Test components in both light and dark themes
- Use appropriate opacity values (`/ 0.5`) for transparency

#### ❌ DON'T
- Hardcode hex values in components
- Use arbitrary color values like `bg-[#123456]`
- Mix different color systems in the same component
- Skip testing theme transitions

### 5. Migration Guide

#### From Hardcoded Colors
```tsx
// ❌ Before
<div style={{ backgroundColor: '#f59e0b' }}>

// ✅ After
<div className="bg-primary">
// or
<div style={{ backgroundColor: 'hsl(var(--primary))' }}>
```

#### From Arbitrary Tailwind Colors
```tsx
// ❌ Before  
<div className="bg-[#D4A574] text-black">

// ✅ After
<div className="bg-omnara-gold text-black">
```

#### From Inline Styles
```tsx
// ❌ Before
<div style={{ color: 'rgba(245, 158, 11, 0.5)' }}>

// ✅ After
<div className="text-primary/50">
// or
<div style={{ color: 'hsl(var(--primary) / 0.5)' }}>
```

## Available Gradients

All gradients are centrally defined and available as Tailwind background utilities:

```tsx
<div className="bg-warm-gradient">       // Warm dark gradient
<div className="bg-amber-glow">          // Amber glow effect
<div className="bg-starfield">           // Starfield background
<div className="bg-terminal-glow">       // Terminal green glow
<div className="bg-hero-gradient">       // Legacy hero gradient
<div className="bg-omnara-card">         // Card gradient
```

## Utility Functions

### Color Conversion
```tsx
import { hexToHsl } from '@/lib/theme/utils';

const hslValue = hexToHsl('#f59e0b'); // "43 91% 48%"
```

### Dynamic Theme Colors
```tsx
import { themeUtils } from '@/lib/theme/utils';

// Get current CSS variable value
const primaryColor = themeUtils.getCSSVariable('--primary');

// Set CSS variable dynamically
themeUtils.setCSSVariable('--primary', '43 91% 48%');
```

## Component Examples

### Button with Brand Colors
```tsx
<Button className="bg-primary hover:bg-primary/90 text-primary-foreground">
  Primary Action
</Button>

<Button className="bg-omnara-gold hover:bg-omnara-gold/90 text-black">
  Omnara Gold Button  
</Button>
```

### Card with Theme Colors
```tsx
<div className="bg-card border border-border rounded-lg p-6">
  <h3 className="text-card-foreground">Card Title</h3>
  <p className="text-muted-foreground">Card description</p>
</div>
```

### Status Indicators
```tsx
<div className="bg-success-100 border border-success-200 text-success-800 p-2 rounded">
  Success message
</div>

<div className="bg-error-100 border border-error-200 text-error-800 p-2 rounded">
  Error message  
</div>
```

## Dark/Light Theme Support

The color system automatically adapts between themes. Colors are defined with appropriate contrast ratios and semantic meanings that work in both contexts.

```tsx
// These automatically adapt to the current theme
<div className="bg-background text-foreground">
<div className="bg-card text-card-foreground">
<div className="border border-border">
```

## Accessibility

- All color combinations meet WCAG contrast requirements
- Focus states use appropriate ring colors
- Error states use both color and text indicators
- Theme switching maintains readability

## Migration Checklist

When updating components to use the centralized color system:

- [ ] Replace hardcoded hex values with semantic tokens
- [ ] Replace `bg-[#...]` arbitrary values with defined classes
- [ ] Update inline styles to use CSS custom properties  
- [ ] Test component in both light and dark themes
- [ ] Verify accessibility contrast ratios
- [ ] Check hover/focus states work correctly

## Future Enhancements

- Color palette generator for new themes
- Automatic color contrast validation
- Design token export for design tools
- Runtime theme customization
- Color blindness simulation tools