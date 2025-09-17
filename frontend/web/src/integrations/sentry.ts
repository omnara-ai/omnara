import * as Sentry from '@sentry/react'

const dsn = import.meta.env.VITE_SENTRY_DSN || import.meta.env.SENTRY_DSN
const sentryEnabled = Boolean(dsn)

if (sentryEnabled) {
  Sentry.init({
    dsn,
    environment: import.meta.env.MODE,
    tracesSampleRate: 0.1,
  })
}

type ErrorMetadata = {
  context?: string
  extras?: Record<string, unknown>
  tags?: Record<string, string>
}

const toError = (error: unknown) => {
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

  if (!sentryEnabled) {
    return
  }

  Sentry.withScope(scope => {
    if (metadata?.extras) {
      scope.setExtras(metadata.extras)
    }
    if (metadata?.tags) {
      Object.entries(metadata.tags).forEach(([key, value]) => {
        scope.setTag(key, value)
      })
    }
    if (metadata?.context) {
      scope.setContext('context', { message: metadata.context })
    }

    Sentry.captureException(toError(error))
  })
}

export const reportMessage = (message: string, metadata?: ErrorMetadata) => {
  console.warn(message, metadata?.extras)

  if (!sentryEnabled) {
    return
  }

  const captureContext: Record<string, unknown> = {}

  if (metadata?.extras) {
    captureContext.extra = metadata.extras
  }
  if (metadata?.tags) {
    captureContext.tags = metadata.tags
  }
  if (metadata?.context) {
    captureContext.contexts = { context: { message: metadata.context } }
  }

  Sentry.captureMessage(message, captureContext)
}

export const isSentryEnabled = sentryEnabled

export { Sentry }
