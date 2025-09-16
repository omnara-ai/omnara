import * as Sentry from 'sentry-expo'

const dsn = process.env.EXPO_PUBLIC_SENTRY_DSN ?? process.env.SENTRY_DSN

if (dsn) {
  Sentry.init({
    dsn,
    enableInExpoDevelopment: true,
    debug: __DEV__,
    tracesSampleRate: 0.1,
  })
}

export const captureException = Sentry.Native.captureException
export const captureMessage = Sentry.Native.captureMessage
