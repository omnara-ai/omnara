import React, { useEffect } from 'react';
import * as Updates from 'expo-updates';
import RootApp from './src/app/_layout';

export default function App() {
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
}
