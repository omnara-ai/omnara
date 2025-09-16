import { useRef, useImperativeHandle, forwardRef } from 'react'
import { YesNoQuestion } from './YesNoQuestion'
import { OptionsQuestion } from './OptionsQuestion'
import { parseQuestionFormat } from '@/utils/questionParser'

interface StructuredQuestionProps {
  questionText: string
  onAnswer: (answer: string) => void
  onAnswerAndSubmit?: (answer: string) => Promise<void>
  onFocusTextInput?: () => void
}

export interface StructuredQuestionRef {
  focusTextInput: () => void
}

export const StructuredQuestion = forwardRef<StructuredQuestionRef, StructuredQuestionProps>(
  ({ questionText, onAnswer, onAnswerAndSubmit, onFocusTextInput }, ref) => {
    useImperativeHandle(ref, () => ({
      focusTextInput: () => {
        // This will be called from parent to focus their textarea
      }
    }))
    
    const parsed = parseQuestionFormat(questionText)
    
    switch (parsed.format) {
      case 'yes-no':
        return (
          <YesNoQuestion
            questionText={parsed.questionText}
            onAnswer={onAnswer}
            onAnswerAndSubmit={onAnswerAndSubmit}
            onFocusTextInput={onFocusTextInput || (() => {})}
          />
        )
        
      case 'options':
        return (
          <OptionsQuestion
            questionText={parsed.questionText}
            options={parsed.options || []}
            onAnswer={onAnswer}
            onAnswerAndSubmit={onAnswerAndSubmit}
          />
        )
        
      case 'open-ended':
      default:
        return null
    }
  }
)

StructuredQuestion.displayName = 'StructuredQuestion'