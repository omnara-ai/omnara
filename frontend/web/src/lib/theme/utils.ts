/**
 * Utility functions for theme management
 */

/**
 * Converts hex color to HSL values for CSS custom properties
 * @param hex - Hex color string (e.g., "#f59e0b")
 * @returns HSL values as string (e.g., "43 91% 48%")
 */
export function hexToHsl(hex: string): string {
  // Remove hash if present
  hex = hex.replace('#', '');
  
  // Convert to RGB
  const r = parseInt(hex.substring(0, 2), 16) / 255;
  const g = parseInt(hex.substring(2, 4), 16) / 255;
  const b = parseInt(hex.substring(4, 6), 16) / 255;
  
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const diff = max - min;
  
  let h = 0;
  let s = 0;
  const l = (max + min) / 2;
  
  if (diff !== 0) {
    s = l > 0.5 ? diff / (2 - max - min) : diff / (max + min);
    
    switch (max) {
      case r:
        h = (g - b) / diff + (g < b ? 6 : 0);
        break;
      case g:
        h = (b - r) / diff + 2;
        break;
      case b:
        h = (r - g) / diff + 4;
        break;
    }
    h /= 6;
  }
  
  return `${Math.round(h * 360)} ${Math.round(s * 100)}% ${Math.round(l * 100)}%`;
}

/**
 * Theme-aware color utilities
 */
export const themeUtils = {
  /**
   * Get CSS custom property value
   */
  getCSSVariable: (property: string): string => {
    if (typeof window === 'undefined') return '';
    return getComputedStyle(document.documentElement).getPropertyValue(property).trim();
  },
  
  /**
   * Set CSS custom property
   */
  setCSSVariable: (property: string, value: string): void => {
    if (typeof window === 'undefined') return;
    document.documentElement.style.setProperty(property, value);
  },
  
  /**
   * Apply theme colors to CSS custom properties
   */
  applyThemeColors: (colors: Record<string, string>): void => {
    Object.entries(colors).forEach(([key, value]) => {
      themeUtils.setCSSVariable(`--${key}`, value);
    });
  }
};

/**
 * Get the appropriate text color for a background color
 */
export function getContrastTextColor(backgroundColor: string): 'white' | 'black' {
  // Convert hex to RGB
  const hex = backgroundColor.replace('#', '');
  const r = parseInt(hex.substring(0, 2), 16);
  const g = parseInt(hex.substring(2, 4), 16);
  const b = parseInt(hex.substring(4, 6), 16);
  
  // Calculate luminance
  const luminance = (0.299 * r + 0.587 * g + 0.114 * b) / 255;
  
  return luminance > 0.5 ? 'black' : 'white';
}