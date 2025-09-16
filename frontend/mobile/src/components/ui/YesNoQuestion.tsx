import React from 'react';
import { View, Text, StyleSheet, TouchableOpacity } from 'react-native';
import { theme } from '@/constants/theme';

interface YesNoQuestionProps {
  questionText: string;
  onAnswer: (answer: string) => void;
  onAnswerAndSubmit?: (answer: string) => Promise<void>;
  onFocusTextInput: () => void;
}

export const YesNoQuestion: React.FC<YesNoQuestionProps> = ({
  questionText,
  onAnswer,
  onAnswerAndSubmit,
  onFocusTextInput,
}) => {
  const handleYes = () => {
    if (onAnswerAndSubmit) {
      onAnswerAndSubmit('Yes');
    } else {
      onAnswer('Yes');
    }
  };
  
  const handleNo = () => {
    if (onAnswerAndSubmit) {
      onAnswerAndSubmit('No');
    } else {
      onAnswer('No');
    }
  };
  
  return (
    <View style={styles.container}>
      <View style={styles.buttonContainer}>
        <TouchableOpacity
          style={styles.button}
          onPress={handleYes}
          activeOpacity={0.7}
        >
          <Text style={styles.buttonText}>Yes</Text>
        </TouchableOpacity>
        <TouchableOpacity
          style={styles.button}
          onPress={handleNo}
          activeOpacity={0.7}
        >
          <Text style={styles.buttonText}>No</Text>
        </TouchableOpacity>
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    // Container spacing handled by parent
  },
  buttonContainer: {
    flexDirection: 'row',
    gap: theme.spacing.xs,
  },
  button: {
    flex: 1,
    backgroundColor: 'rgba(255, 255, 255, 0.1)',
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.2)',
    borderRadius: theme.borderRadius.sm,
    paddingVertical: theme.spacing.sm,
    paddingHorizontal: theme.spacing.md,
    alignItems: 'center',
  },
  buttonText: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.9)',
  },
});