import { Button } from '@/components/ui/button'

interface OptionsQuestionProps {
  questionText: string
  options: string[]
  onAnswer: (answer: string) => void
  onAnswerAndSubmit?: (answer: string) => Promise<void>
}

export function OptionsQuestion({ questionText, options, onAnswer, onAnswerAndSubmit }: OptionsQuestionProps) {
  const handleOptionClick = (option: string) => {
    if (onAnswerAndSubmit) {
      onAnswerAndSubmit(option)
    } else {
      onAnswer(option)
    }
  }
  
  return (
    <div className="grid grid-cols-1 gap-2">
      {options.map((option, index) => (
        <Button
          key={index}
          size="sm"
          variant="outline"
          onClick={() => handleOptionClick(option)}
          className="justify-start text-left border-cozy-amber/20 text-cream bg-warm-charcoal/30 hover:bg-cozy-amber/20 hover:border-cozy-amber/50 hover:text-soft-gold transition-all duration-300 font-mono text-sm"
        >
          <span className="font-medium mr-2">{index + 1}.</span>
          {option}
        </Button>
      ))}
    </div>
  )
}