import * as Sentry from '@sentry/react-native';

export type ErrorMetadata = {
  context?: string;
  extras?: Record<string, unknown>;
  tags?: Record<string, string>;
};

const isMetadata = (value: unknown): value is ErrorMetadata =>
  !!value && typeof value === 'object' && !Array.isArray(value) && (
    'context' in (value as Record<string, unknown>) ||
    'extras' in (value as Record<string, unknown>) ||
    'tags' in (value as Record<string, unknown>)
  );

const getSentryClient = () => {
  try {
    return Sentry.getCurrentHub().getClient();
  } catch (error) {
    console.warn('[logger] Unable to access Sentry client', error);
    return null;
  }
};

const normalizeError = (error: unknown): Error => {
  if (error instanceof Error) {
    return error;
  }

  if (typeof error === 'string') {
    return new Error(error);
  }

  try {
    return new Error(JSON.stringify(error));
  } catch (_error) {
    return new Error('Unknown error');
  }
};

export function reportError(...args: unknown[]): void {
  if (args.length === 0) {
    console.error('Unknown error');
    return;
  }

  let metadata: ErrorMetadata | undefined;
  let callArgs = [...args];

  const potentialMetadata = callArgs[callArgs.length - 1];
  if (isMetadata(potentialMetadata)) {
    metadata = potentialMetadata;
    callArgs = callArgs.slice(0, -1);
  }

  const logArgs = metadata
    ? [
        ...(metadata.context ? [metadata.context] : []),
        ...callArgs,
        ...(metadata.extras ? [metadata.extras] : []),
      ]
    : callArgs;

  console.error(...logArgs);

  const client = getSentryClient();
  if (!client) {
    return;
  }

  let capturedError = callArgs.find((arg) => arg instanceof Error) as Error | undefined;
  if (!capturedError) {
    const messageArg = callArgs.find((arg) => typeof arg === 'string') as string | undefined;
    capturedError = normalizeError(messageArg ?? 'Unknown error');
  }

  Sentry.withScope((scope) => {
    if (metadata?.extras) {
      scope.setExtras(metadata.extras);
    }
    if (metadata?.tags) {
      Object.entries(metadata.tags).forEach(([key, value]) => scope.setTag(key, value));
    }
    if (metadata?.context) {
      scope.setContext('context', { message: metadata.context });
    }

    scope.setLevel('error');
    Sentry.captureException(capturedError as Error);
  });
}

export function reportMessage(message: string, metadata?: ErrorMetadata): void {
  if (metadata?.context) {
    console.warn(`${metadata.context}: ${message}`, metadata.extras);
  } else {
    console.warn(message, metadata?.extras);
  }

  const client = getSentryClient();
  if (!client) {
    return;
  }

  Sentry.withScope((scope) => {
    if (metadata?.extras) {
      scope.setExtras(metadata.extras);
    }
    if (metadata?.tags) {
      Object.entries(metadata.tags).forEach(([key, value]) => scope.setTag(key, value));
    }
    if (metadata?.context) {
      scope.setContext('context', { message: metadata.context });
    }

    scope.setLevel('warning');
    Sentry.captureMessage(message);
  });
}

export const captureException = reportError;
export const captureMessage = reportMessage;
