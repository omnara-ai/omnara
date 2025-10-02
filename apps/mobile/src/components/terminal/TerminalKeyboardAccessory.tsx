import React from 'react';
import { View, TouchableOpacity, Text, StyleSheet } from 'react-native';
import { ChevronUp, ChevronDown, CornerDownLeft, KeyboardIcon } from 'lucide-react-native';
import { theme } from '@/constants/theme';

interface TerminalKeyboardAccessoryProps {
  onKeyPress: (sequence: string) => void;
  onDismissKeyboard: () => void;
  keyboardVisible: boolean;
  disabled?: boolean;
}

const KEY_SEQUENCES = {
  ARROW_UP: '\x1b[A',
  ARROW_DOWN: '\x1b[B',
  ESCAPE: '\x1b',
  CTRL_C: '\x03',
  ENTER: '\r',
};

export const TerminalKeyboardAccessory: React.FC<TerminalKeyboardAccessoryProps> = ({
  onKeyPress,
  onDismissKeyboard,
  keyboardVisible,
  disabled = false,
}) => {
  const handleKeyPress = (sequence: string) => {
    if (!disabled) {
      onKeyPress(sequence);
    }
  };

  return (
    <View style={styles.container}>
      {/* Left section: Special keys */}
      <View style={styles.leftSection}>
        {keyboardVisible && (
          <TouchableOpacity
            style={[styles.button, styles.iconButton]}
            onPress={onDismissKeyboard}
            activeOpacity={0.7}
          >
            <KeyboardIcon
              size={18}
              color={theme.colors.white}
            />
          </TouchableOpacity>
        )}

        <TouchableOpacity
          style={[styles.button, styles.textButton, disabled && styles.buttonDisabled]}
          onPress={() => handleKeyPress(KEY_SEQUENCES.ESCAPE)}
          disabled={disabled}
          activeOpacity={0.7}
        >
          <Text style={[styles.buttonText, disabled && styles.buttonTextDisabled]}>Esc</Text>
        </TouchableOpacity>

        <TouchableOpacity
          style={[styles.button, styles.textButton, disabled && styles.buttonDisabled]}
          onPress={() => handleKeyPress(KEY_SEQUENCES.CTRL_C)}
          disabled={disabled}
          activeOpacity={0.7}
        >
          <Text style={[styles.buttonText, disabled && styles.buttonTextDisabled]}>^C</Text>
        </TouchableOpacity>
      </View>

      {/* Right section: Arrow keys and Enter */}
      <View style={styles.rightSection}>
        <View style={styles.verticalArrows}>
          <TouchableOpacity
            style={[styles.button, styles.iconButton, styles.verticalButton, disabled && styles.buttonDisabled]}
            onPress={() => handleKeyPress(KEY_SEQUENCES.ARROW_UP)}
            disabled={disabled}
            activeOpacity={0.7}
          >
            <ChevronUp
              size={20}
              color={disabled ? theme.colors.textMuted : theme.colors.white}
            />
          </TouchableOpacity>

          <TouchableOpacity
            style={[styles.button, styles.iconButton, styles.verticalButton, disabled && styles.buttonDisabled]}
            onPress={() => handleKeyPress(KEY_SEQUENCES.ARROW_DOWN)}
            disabled={disabled}
            activeOpacity={0.7}
          >
            <ChevronDown
              size={20}
              color={disabled ? theme.colors.textMuted : theme.colors.white}
            />
          </TouchableOpacity>
        </View>

        <TouchableOpacity
          style={[styles.button, styles.enterButton, disabled && styles.buttonDisabled]}
          onPress={() => handleKeyPress(KEY_SEQUENCES.ENTER)}
          disabled={disabled}
          activeOpacity={0.7}
        >
          <CornerDownLeft
            size={20}
            color={disabled ? theme.colors.textMuted : theme.colors.white}
          />
        </TouchableOpacity>
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    backgroundColor: 'rgba(0, 0, 0, 0.9)',
    paddingVertical: 10,
    paddingHorizontal: 12,
    borderTopWidth: 1,
    borderTopColor: 'rgba(255, 255, 255, 0.1)',
  },
  leftSection: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
  },
  rightSection: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
  },
  verticalArrows: {
    flexDirection: 'column',
    gap: 4,
  },
  button: {
    justifyContent: 'center',
    alignItems: 'center',
    borderRadius: 8,
    backgroundColor: 'rgba(255, 255, 255, 0.08)',
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.15)',
  },
  textButton: {
    paddingHorizontal: 14,
    paddingVertical: 10,
    minWidth: 52,
  },
  iconButton: {
    width: 40,
    height: 28,
  },
  verticalButton: {
    height: 28,
  },
  enterButton: {
    width: 52,
    height: 60,
  },
  buttonDisabled: {
    backgroundColor: 'rgba(255, 255, 255, 0.03)',
    borderColor: 'rgba(255, 255, 255, 0.05)',
  },
  buttonText: {
    color: theme.colors.white,
    fontSize: 13,
    fontFamily: theme.fontFamily.medium,
    fontWeight: '600' as any,
  },
  buttonTextDisabled: {
    color: theme.colors.textMuted,
  },
});