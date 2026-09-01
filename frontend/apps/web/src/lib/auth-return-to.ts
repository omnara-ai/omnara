export function safeReturnTo(value: string | null): string {
  if (!value || !value.startsWith('/') || value.startsWith('//') || value.includes('\\')) return '/'
  try {
    const target = new URL(value, window.location.origin)
    if (target.origin !== window.location.origin) return '/'
    const path = `${target.pathname}${target.search}${target.hash}`
    if (!path.startsWith('/') || path.startsWith('//') || path.includes('\\')) return '/'
    if (target.pathname.startsWith('/login')) return '/'
    return path
  } catch {
    return '/'
  }
}
