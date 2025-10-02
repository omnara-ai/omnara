import React, { useRef, useCallback, useEffect, useState } from 'react';
import { View, StyleSheet, Keyboard, Platform, Animated } from 'react-native';

import { TerminalMobileTerminal, TerminalMobileTerminalRef } from './TerminalMobileTerminal';
import { TerminalKeyboardAccessory } from './TerminalKeyboardAccessory';

interface TerminalInstancePanelProps {
  instanceId: string;
}

export const TerminalInstancePanel: React.FC<TerminalInstancePanelProps> = ({ instanceId }) => {
  const terminalRef = useRef<TerminalMobileTerminalRef>(null);
  const [keyboardHeight] = useState(new Animated.Value(0));
  const [keyboardVisible, setKeyboardVisible] = useState(false);

  const handleKeyPress = useCallback((sequence: string) => {
    terminalRef.current?.sendKeySequence(sequence);
  }, []);

  const handleDismissKeyboard = useCallback(() => {
    // Blur the WebView textarea to dismiss keyboard
    if (terminalRef.current) {
      terminalRef.current.blurTerminal();
    }
    Keyboard.dismiss();
  }, []);

  useEffect(() => {
    const showSubscription = Keyboard.addListener(
      Platform.OS === 'ios' ? 'keyboardWillShow' : 'keyboardDidShow',
      (e) => {
        setKeyboardVisible(true);
        Animated.timing(keyboardHeight, {
          toValue: e.endCoordinates.height,
          duration: Platform.OS === 'ios' ? e.duration : 250,
          useNativeDriver: false,
        }).start();
      }
    );

    const hideSubscription = Keyboard.addListener(
      Platform.OS === 'ios' ? 'keyboardWillHide' : 'keyboardDidHide',
      (e) => {
        setKeyboardVisible(false);
        Animated.timing(keyboardHeight, {
          toValue: 0,
          duration: Platform.OS === 'ios' ? e.duration : 250,
          useNativeDriver: false,
        }).start();
      }
    );

    return () => {
      showSubscription.remove();
      hideSubscription.remove();
    };
  }, [keyboardHeight]);

  return (
    <Animated.View style={[styles.container, { marginBottom: keyboardHeight }]}>
      <View style={styles.terminalWrapper}>
        <TerminalMobileTerminal ref={terminalRef} instanceId={instanceId} />
      </View>
      <TerminalKeyboardAccessory
        onKeyPress={handleKeyPress}
        onDismissKeyboard={handleDismissKeyboard}
        keyboardVisible={keyboardVisible}
      />
    </Animated.View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    marginTop: 16,
  },
  terminalWrapper: {
    flex: 1,
  },
});
