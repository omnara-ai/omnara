import { useEffect, useRef } from 'react'
import { useLocation } from 'react-router-dom'
import { analytics } from '@/lib/analytics'

export function usePageViewTracking() {
  const location = useLocation()
  const previousPath = useRef<string>('')

  useEffect(() => {
    const currentPath = location.pathname + location.search + location.hash

    // Don't track the same page twice
    if (currentPath === previousPath.current) {
      return
    }

    // Track page view
    analytics.trackPageView(location.pathname, {
      page: location.pathname,
      section: getPageSection(location.pathname),
      source: document.referrer || 'direct',
    })

    // Update previous path
    previousPath.current = currentPath
  }, [location])
}

function getPageSection(pathname: string): string {
  if (pathname === '/') return 'landing'
  if (pathname.startsWith('/dashboard')) {
    const segments = pathname.split('/')
    if (segments.length === 2) return 'command_center'
    if (segments[2] === 'instances') return 'instances'
    if (segments[2] === 'billing') return 'billing'
    if (segments[2] === 'settings') return 'settings'
    if (segments[2] === 'api-keys') return 'api_keys'
    if (segments[2] === 'user-agents') return 'agents'
    return 'dashboard'
  }
  if (pathname === '/pricing') return 'pricing'
  if (pathname === '/privacy') return 'privacy'
  if (pathname === '/terms') return 'terms'
  if (pathname === '/cli-auth') return 'cli_auth'
  return 'other'
}