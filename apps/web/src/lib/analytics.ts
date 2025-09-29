import { usePostHog } from 'posthog-js/react'

// Event types for type safety
export type AnalyticsEvent =
  // Authentication events
  | 'user_signed_up'
  | 'user_signed_in'
  | 'user_signed_out'

  // Agent management events
  | 'agent_launched'
  | 'webhook_configured'
  | 'api_key_generated'

  // Dashboard interactions
  | 'command_palette_opened'
  | 'instance_viewed'
  | 'message_sent'

  // Navigation events
  | 'page_viewed'
  | 'onboarding_completed'

  // Other events
  | 'error_occurred'

export interface UserProperties {
  email?: string
  created_at?: string
  is_mobile?: boolean
}

export interface EventProperties {
  // Common properties
  page?: string
  section?: string
  source?: string

  // Authentication specific
  auth_method?: 'email' | 'google' | 'github' | 'apple' | 'oauth'

  // Agent specific
  agent_type?: string
  prompt_type?: string
  integration_type?: string

  // Instance specific
  instance_id?: string
  instance_status?: string

  // Interaction specific
  action_type?: string
  feature_name?: string

  // Error specific
  error_type?: string
  error_message?: string
  error_stack?: string

  // Allow additional properties per-event without type errors
  [key: string]: unknown
}

class AnalyticsService {
  private posthog: unknown = null

  initialize(posthogInstance: unknown) {
    this.posthog = posthogInstance
  }

  // Track events
  track(event: AnalyticsEvent, properties?: EventProperties) {
    if (!this.posthog || typeof this.posthog !== 'object') {
      console.warn('[Analytics] PostHog not initialized')
      return
    }

    const posthog = this.posthog as { capture: (event: string, properties: Record<string, unknown>) => void }
    posthog.capture(event, {
      ...properties,
      timestamp: new Date().toISOString(),
    })
  }

  // Identify users
  identify(userId: string, properties?: UserProperties) {
    if (!this.posthog || typeof this.posthog !== 'object') {
      console.warn('[Analytics] PostHog not initialized')
      return
    }

    const posthog = this.posthog as { identify: (userId: string, properties?: UserProperties) => void }
    posthog.identify(userId, properties)
  }

  // Track page views
  trackPageView(page: string, properties?: EventProperties) {
    this.track('page_viewed', {
      page,
      ...properties
    })
  }

  // Reset user (on logout)
  reset() {
    if (!this.posthog || typeof this.posthog !== 'object') {
      console.warn('[Analytics] PostHog not initialized')
      return
    }

    const posthog = this.posthog as { reset: () => void }
    posthog.reset()
  }
}

// Singleton instance
export const analytics = new AnalyticsService()

// React hook for analytics
export function useAnalytics() {
  const posthog = usePostHog()

  // Initialize analytics service with PostHog instance
  if (posthog && !analytics['posthog']) {
    analytics.initialize(posthog)
  }

  return {
    track: analytics.track.bind(analytics),
    identify: analytics.identify.bind(analytics),
    trackPageView: analytics.trackPageView.bind(analytics),
    reset: analytics.reset.bind(analytics),
  }
}

// Utility functions for common tracking patterns
export const trackError = (error: Error, context?: string, additionalData?: Record<string, unknown>) => {
  analytics.track('error_occurred', {
    error_type: error.name,
    error_message: error.message,
    error_stack: error.stack,
    section: context,
    ...additionalData,
  })
}

export const isMobile = () => {
  return window.innerWidth < 768
}

export const getDeviceType = () => {
  return isMobile() ? 'mobile' : 'desktop'
}
