const BASE = (import.meta.env.VITE_PUBLIC_ASSETS_BASE_URL || '').replace(/\/+$/, '');

/**
 * Build a public asset URL using the configured base.
 * Accepts paths with or without a leading slash.
 */
export function asset(path: string): string {
  const normalized = path.startsWith('/') ? path : `/${path}`;
  // If BASE is empty (not set), fall back to origin-relative path
  return `${BASE}${normalized}`;
}

