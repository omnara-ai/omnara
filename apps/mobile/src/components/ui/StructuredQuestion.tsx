import React, { forwardRef, useImperativeHandle } from 'react';
import { View, Text, StyleSheet } from 'react-native';
import { YesNoQuestion } from './YesNoQuestion';
import { OptionsQuestion } from './OptionsQuestion';
import { parseQuestionFormat } from '@/utils/questionParser';
import { theme } from '@/constants/theme';

interface StructuredQuestionProps {
  questionText: string;
  onAnswer: (answer: string) => void;
  onAnswerAndSubmit?: (answer: string) => Promise<void>;
  onFocusTextInput: () => void;
}

export interface StructuredQuestionRef {
  // Placeholder for any methods we might need to expose
}

export const StructuredQuestion = forwardRef<StructuredQuestionRef, StructuredQuestionProps>(
  ({ questionText, onAnswer, onAnswerAndSubmit, onFocusTextInput }, ref) => {
    useImperativeHandle(ref, () => ({}));
    
    const parsed = parseQuestionFormat(questionText);
    
    switch (parsed.format) {
      case 'yes-no':
        return (
          <YesNoQuestion
            questionText={parsed.questionText}
            onAnswer={onAnswer}
            onAnswerAndSubmit={onAnswerAndSubmit}
            onFocusTextInput={onFocusTextInput}
          />
        );
        
      case 'options':
        return (
          <OptionsQuestion
            questionText={parsed.questionText}
            options={parsed.options || []}
            onAnswer={onAnswer}
            onAnswerAndSubmit={onAnswerAndSubmit}
          />
        );
        
      case 'open-ended':
      default:
        return null;
    }
  }
);

StructuredQuestion.displayName = 'StructuredQuestion';

const styles = StyleSheet.create({
  container: {
    marginBottom: theme.spacing.md,
  },
  questionText: {
    fontSize: theme.fontSize.base,
    fontFamily: theme.fontFamily.regular,
    color: theme.colors.white,
    lineHeight: theme.fontSize.base * 1.5,
  },
});