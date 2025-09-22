import * as Sentry from '@sentry/react'

const dsn = import.meta.env.VITE_SENTRY_DSN || import.meta.env.SENTRY_DSN

export const isSentryEnabled = Boolean(dsn)

if (isSentryEnabled) {
  Sentry.init({
    dsn,
    environment: import.meta.env.MODE,
    tracesSampleRate: 0.1,
  })
}

export type ErrorMetadata = {
  context?: string
  extras?: Record<string, unknown>
  tags?: Record<string, string>
}

const normalizeError = (error: unknown): Error => {
  if (error instanceof Error) {
    return error
  }

  if (typeof error === 'string') {
    return new Error(error)
  }

  try {
    return new Error(JSON.stringify(error))
  } catch (_) {
    return new Error('Unknown error')
  }
}

export const reportError = (error: unknown, metadata?: ErrorMetadata) => {
  if (metadata?.context) {
    console.error(metadata.context, error, metadata.extras)
  } else {
    console.error(error)
  }

  if (!isSentryEnabled) {
    return
  }

  const normalizedError = normalizeError(error)

  try {
    Sentry.withScope(scope => {
      if (metadata?.extras) {
        scope.setExtras(metadata.extras)
      }
      if (metadata?.tags) {
        Object.entries(metadata.tags).forEach(([key, value]) => scope.setTag(key, value))
      }
      if (metadata?.context) {
        scope.setContext('context', { message: metadata.context })
      }

      scope.setLevel('error')
      Sentry.captureException(normalizedError)
    })
  } catch (captureError) {
    console.warn('[sentry] Failed to capture exception', captureError)
  }
}

export const reportMessage = (message: string, metadata?: ErrorMetadata) => {
  if (metadata?.context) {
    console.warn(`${metadata.context}: ${message}`, metadata.extras)
  } else {
    console.warn(message, metadata?.extras)
  }

  if (!isSentryEnabled) {
    return
  }

  try {
    Sentry.withScope(scope => {
      if (metadata?.extras) {
        scope.setExtras(metadata.extras)
      }
      if (metadata?.tags) {
        Object.entries(metadata.tags).forEach(([key, value]) => scope.setTag(key, value))
      }
      if (metadata?.context) {
        scope.setContext('context', { message: metadata.context })
      }

      scope.setLevel('warning')
      Sentry.captureMessage(message)
    })
  } catch (captureError) {
    console.warn('[sentry] Failed to capture message', captureError)
  }
}

export { Sentry }
