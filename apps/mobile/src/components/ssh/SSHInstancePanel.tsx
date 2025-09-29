import React from 'react';
import { View, StyleSheet } from 'react-native';

import { SSHMobileTerminal } from './SSHMobileTerminal';

interface SSHInstancePanelProps {
  instanceId: string;
}

export const SSHInstancePanel: React.FC<SSHInstancePanelProps> = ({ instanceId }) => {
  return (
    <View style={styles.container}>
      <SSHMobileTerminal instanceId={instanceId} />
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    marginTop: 16,
  },
});
