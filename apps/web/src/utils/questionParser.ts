export type QuestionFormat = 'yes-no' | 'options' | 'open-ended'

export interface ParsedQuestion {
  format: QuestionFormat
  questionText: string
  options?: string[]
}

export function parseQuestionFormat(text: string): ParsedQuestion {
  // Check for [YES/NO] at the end
  const yesNoRegex = /^([\s\S]*?)\n*\[YES\/NO\]\s*$/
  const yesNoMatch = text.match(yesNoRegex)
  
  if (yesNoMatch) {
    return {
      format: 'yes-no',
      questionText: yesNoMatch[1].trim()
    }
  }
  
  // Check for [OPTIONS] block at the end
  const optionsRegex = /^([\s\S]*?)\n*\[OPTIONS\]\n*([\s\S]+?)\n*\[\/OPTIONS\]\s*$/
  const optionsMatch = text.match(optionsRegex)
  
  if (optionsMatch) {
    const questionText = optionsMatch[1].trim()
    const optionsBlock = optionsMatch[2]
    const options = parseOptions(optionsBlock)
    
    return {
      format: 'options',
      questionText,
      options
    }
  }
  
  // Default to open-ended
  return {
    format: 'open-ended',
    questionText: text.trim()
  }
}

function parseOptions(optionsBlock: string): string[] {
  const lines = optionsBlock.trim().split('\n')
  const options: string[] = []
  
  for (const line of lines) {
    const match = line.match(/^\d+\.\s*(.+)$/)
    if (match) {
      options.push(match[1].trim())
    }
  }
  
  return options
}