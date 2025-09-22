import React from 'react';
import { View, Text, StyleSheet, ScrollView, TouchableOpacity } from 'react-native';
import { theme } from '@/constants/theme';

interface OptionsQuestionProps {
  questionText: string;
  options: string[];
  onAnswer: (answer: string) => void;
  onAnswerAndSubmit?: (answer: string) => Promise<void>;
}

export const OptionsQuestion: React.FC<OptionsQuestionProps> = ({
  questionText,
  options,
  onAnswer,
  onAnswerAndSubmit,
}) => {
  const handleOptionSelect = (option: string) => {
    if (onAnswerAndSubmit) {
      onAnswerAndSubmit(option);
    } else {
      onAnswer(option);
    }
  };
  
  return (
    <View style={styles.container}>
      <ScrollView 
        style={styles.optionsContainer}
        showsVerticalScrollIndicator={false}
      >
        {options.map((option, index) => (
          <TouchableOpacity
            key={index}
            style={styles.optionButton}
            onPress={() => handleOptionSelect(option)}
            activeOpacity={0.7}
          >
            <Text style={styles.optionNumber}>{index + 1}.</Text>
            <Text style={styles.optionText}>{option}</Text>
          </TouchableOpacity>
        ))}
      </ScrollView>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    // Container spacing handled by parent
  },
  optionsContainer: {
    maxHeight: 200, // Reduced height for more compact display
  },
  optionButton: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: 'rgba(255, 255, 255, 0.1)',
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.2)',
    borderRadius: theme.borderRadius.sm,
    paddingVertical: theme.spacing.sm,
    paddingHorizontal: theme.spacing.md,
    marginBottom: theme.spacing.xs,
  },
  optionNumber: {
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.medium,
    fontWeight: theme.fontWeight.medium as any,
    color: theme.colors.white,
    marginRight: theme.spacing.sm,
  },
  optionText: {
    flex: 1,
    fontSize: theme.fontSize.sm,
    fontFamily: theme.fontFamily.regular,
    color: 'rgba(255, 255, 255, 0.9)',
    lineHeight: 18,
  },
});