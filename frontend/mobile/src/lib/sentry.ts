import * as Sentry from 'sentry-expo'

const dsn = process.env.EXPO_PUBLIC_SENTRY_DSN ?? process.env.SENTRY_DSN
const sentryEnabled = Boolean(dsn)

if (sentryEnabled) {
  Sentry.init({
    dsn,
    enableInExpoDevelopment: true,
    debug: __DEV__,
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

const buildCaptureContext = (metadata?: ErrorMetadata) => {
  const context: Record<string, unknown> = {}

  if (metadata?.extras) {
    context.extra = metadata.extras
  }

  if (metadata?.tags) {
    context.tags = metadata.tags
  }

  if (metadata?.context) {
    context.contexts = { context: { message: metadata.context } }
  }

  return context
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

  Sentry.Native.captureException(toError(error), buildCaptureContext(metadata))
}

export const reportMessage = (message: string, metadata?: ErrorMetadata) => {
  console.warn(message, metadata?.extras)

  if (!sentryEnabled) {
    return
  }

  Sentry.Native.captureMessage(message, buildCaptureContext(metadata))
}

export const captureException = Sentry.Native.captureException
export const captureMessage = Sentry.Native.captureMessage
export const isSentryEnabled = sentryEnabled
