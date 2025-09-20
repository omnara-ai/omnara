/*
 * Some bundled dependencies (notably older Sentry packages) default-import the
 * CommonJS build of `tslib`, expecting a `default` export. Hermes evaluates the
 * module without adding that property, so those imports end up undefined and the
 * helpers such as `__extends` are missing. We eagerly add the expected default
 * property before those packages load.
 */

/* eslint-disable @typescript-eslint/no-var-requires */
type TslibModule = typeof import('tslib') & { default?: unknown };

const ensureInterop = (runtime: TslibModule | undefined | null) => {
  if (!runtime) {
    return;
  }

  const runtimeRecord = runtime as Record<string, unknown>;

  if (runtimeRecord.default == null) {
    runtimeRecord.default = runtime;
  }

  if (runtimeRecord.__esModule !== true) {
    runtimeRecord.__esModule = true;
  }
};

const tryEnsureInterop = (load: () => TslibModule, label: string) => {
  try {
    ensureInterop(load());
  } catch (error) {
    if (__DEV__) {
      // eslint-disable-next-line no-console
      console.debug('[tslibInteropFix] Failed to preload', label, error);
    }
  }
};

tryEnsureInterop(() => require('tslib'), 'tslib');
tryEnsureInterop(() => require('sentry-expo/node_modules/tslib/tslib.js'), 'sentry-expo');
tryEnsureInterop(() => require('@sentry/core/node_modules/tslib/tslib.js'), '@sentry/core');
tryEnsureInterop(() => require('@sentry/utils/node_modules/tslib/tslib.js'), '@sentry/utils');
tryEnsureInterop(() => require('@sentry/integrations/node_modules/tslib/tslib.js'), '@sentry/integrations');
tryEnsureInterop(() => require('@sentry/browser/node_modules/tslib/tslib.js'), '@sentry/browser');
tryEnsureInterop(() => require('@sentry/hub/node_modules/tslib/tslib.js'), '@sentry/hub');
tryEnsureInterop(() => require('@sentry-internal/tracing/node_modules/tslib/tslib.js'), '@sentry-internal/tracing');
