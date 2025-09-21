import React, { useEffect } from 'react';
import * as Updates from 'expo-updates';
import RootApp from './src/app/_layout';
import * as Sentry from '@sentry/react-native';

const sentryDsn = process.env.EXPO_PUBLIC_SENTRY_DSN;
const sentryEnabled = Boolean(sentryDsn);

if (sentryEnabled) {
  Sentry.init({
    dsn: sentryDsn,
    sendDefaultPii: true,
    enableLogs: __DEV__,
    replaysSessionSampleRate: 0.1,
    replaysOnErrorSampleRate: 1,
    integrations: [Sentry.mobileReplayIntegration(), Sentry.feedbackIntegration()],
  });
}

const App = () => {
  useEffect(() => {
    async function checkForUpdates() {
      if (__DEV__) {
        return;
      }

      try {
        const update = await Updates.checkForUpdateAsync();
        if (update.isAvailable) {
          await Updates.fetchUpdateAsync();
          await Updates.reloadAsync();
        }
      } catch (error) {
        // Silent fail - app continues with current version
        console.log('Update check failed:', error);
      }
    }

    checkForUpdates();
  }, []);

  return <RootApp />;
};

export default sentryEnabled ? Sentry.wrap(App) : App;
