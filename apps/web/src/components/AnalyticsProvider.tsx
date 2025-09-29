import { ReactNode } from 'react'
import { usePageViewTracking } from '@/hooks/usePageViewTracking'
import { useAnalytics } from '@/lib/analytics'

interface AnalyticsProviderProps {
  children: ReactNode
}

export function AnalyticsProvider({ children }: AnalyticsProviderProps) {
  // Ensure analytics is initialized with the PostHog instance
  useAnalytics()
  // Track page views automatically
  usePageViewTracking()

  return <>{children}</>
}
