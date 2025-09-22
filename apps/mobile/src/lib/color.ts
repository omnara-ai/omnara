export function withAlpha(color: string, alpha: number): string {
  // If already rgba(...) just replace alpha
  const rgbaMatch = color.match(/^rgba?\((\d+)\s*,\s*(\d+)\s*,\s*(\d+)(?:\s*,\s*(\d*\.?\d+))?\)$/i);
  if (rgbaMatch) {
    const [_, r, g, b] = rgbaMatch;
    return `rgba(${r}, ${g}, ${b}, ${alpha})`;
  }

  // Normalize hex: #RGB, #RRGGBB, #RRGGBBAA
  const hex = color.trim();
  if (!hex.startsWith('#')) return color; // fallback

  let r = 0, g = 0, b = 0;
  if (hex.length === 4) {
    r = parseInt(hex[1] + hex[1], 16);
    g = parseInt(hex[2] + hex[2], 16);
    b = parseInt(hex[3] + hex[3], 16);
  } else if (hex.length === 7 || hex.length === 9) {
    r = parseInt(hex.slice(1, 3), 16);
    g = parseInt(hex.slice(3, 5), 16);
    b = parseInt(hex.slice(5, 7), 16);
  } else {
    return color; // unsupported format
  }

  return `rgba(${r}, ${g}, ${b}, ${alpha})`;
}

