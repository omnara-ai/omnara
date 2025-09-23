import { Button } from '@/components/ui/button'

interface YesNoQuestionProps {
  questionText: string
  onAnswer: (answer: string) => void
  onAnswerAndSubmit?: (answer: string) => Promise<void>
  onFocusTextInput: () => void
}

export function YesNoQuestion({ questionText, onAnswer, onAnswerAndSubmit, onFocusTextInput }: YesNoQuestionProps) {
  const handleYes = () => {
    if (onAnswerAndSubmit) {
      onAnswerAndSubmit('Yes')
    } else {
      onAnswer('Yes')
    }
  }
  
  const handleNo = () => {
    if (onAnswerAndSubmit) {
      onAnswerAndSubmit('No')
    } else {
      // Populate "No" in the text input and focus it
      onAnswer('No')
      onFocusTextInput()
    }
  }
  
  return (
    <div className="flex gap-2">
      <Button
        size="sm"
        variant="outline"
        onClick={handleYes}
        className="border-sage-green/30 text-sage-green bg-sage-green/10 hover:bg-sage-green/20 hover:text-cream hover:border-sage-green/50 transition-all duration-300 font-mono"
      >
        Yes
      </Button>
      <Button
        size="sm"
        variant="outline"
        onClick={handleNo}
        className="border-terracotta/30 text-terracotta bg-terracotta/10 hover:bg-terracotta/20 hover:text-cream hover:border-terracotta/50 transition-all duration-300 font-mono"
      >
        No
      </Button>
    </div>
  )
}